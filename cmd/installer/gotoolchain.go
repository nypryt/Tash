package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tash/tash/internal/config"
)

// defaultGoVersion is the toolchain the installer offers. It matches the
// version used to build release artifacts.
const defaultGoVersion = "1.27.3"

var goHTTPClient = &http.Client{Timeout: 20 * time.Minute}

type goReleaseFile struct {
	Filename string `json:"filename"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
	Kind     string `json:"kind"`
}

type goRelease struct {
	Version string          `json:"version"`
	Stable  bool            `json:"stable"`
	Files   []goReleaseFile `json:"files"`
}

func goToolchainDir(prefix string) string {
	return filepath.Join(filepath.Dir(prefix), "go")
}

func goArchiveName(version string) (string, error) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "go")
	if version == "" {
		version = defaultGoVersion
	}
	arch := runtime.GOARCH
	switch runtime.GOOS {
	case "windows":
		if arch == "amd64" || arch == "arm64" || arch == "386" {
			return fmt.Sprintf("go%s.windows-%s.zip", version, arch), nil
		}
	case "linux":
		if arch == "amd64" || arch == "arm64" || arch == "386" || arch == "armv6l" {
			return fmt.Sprintf("go%s.linux-%s.tar.gz", version, arch), nil
		}
	case "darwin":
		if arch == "amd64" || arch == "arm64" {
			return fmt.Sprintf("go%s.darwin-%s.tar.gz", version, arch), nil
		}
	}
	return "", fmt.Errorf("no official Go toolchain for %s/%s", runtime.GOOS, arch)
}

// installGoToolchain downloads the official Go distribution and installs it
// next to the Tash prefix (for example ~/.local/go or %LOCALAPPDATA%\Programs\go).
func installGoToolchain(prefix, version string) error {
	if config.IsTermux() {
		return fmt.Errorf("Termux: run `pkg install golang` instead of downloading the desktop toolchain")
	}
	if path, err := exec.LookPath("go"); err == nil {
		fmt.Printf("  ✓ Go is already installed: %s\n", path)
		return nil
	}
	archive, err := goArchiveName(version)
	if err != nil {
		return err
	}
	version = strings.TrimPrefix(strings.TrimSpace(version), "go")
	if version == "" {
		version = defaultGoVersion
	}
	url := "https://go.dev/dl/" + archive
	fmt.Printf("  → downloading Go %s\n    %s\n", version, url)
	path, err := downloadGoArchive(url, archive)
	if err != nil {
		return err
	}
	defer os.Remove(path)

	if want, err := fetchGoChecksum(version, archive); err != nil {
		fmt.Fprintln(os.Stderr, "  ! checksum lookup failed:", err)
	} else if want != "" {
		if err := verifySHA256(path, want); err != nil {
			return err
		}
		fmt.Println("  ✓ checksum verified")
	}

	target := goToolchainDir(prefix)
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	if err := extractGoArchive(path, target); err != nil {
		return err
	}
	goBin := filepath.Join(target, "bin", "go"+executableSuffix())
	if _, err := os.Stat(goBin); err != nil {
		return fmt.Errorf("Go archive did not contain %s", goBin)
	}
	if !config.IsTermux() {
		if _, err := addToUserPath(filepath.Join(target, "bin")); err != nil {
			fmt.Fprintln(os.Stderr, "  ! could not add Go to PATH:", err)
		}
	}
	out, err := exec.Command(goBin, "version").Output()
	if err != nil {
		return fmt.Errorf("installed Go did not run: %w", err)
	}
	fmt.Printf("  ✓ Go installed: %s\n", strings.TrimSpace(string(out)))
	return nil
}

func downloadGoArchive(url, name string) (string, error) {
	resp, err := goHTTPClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("download Go: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download Go: %s", resp.Status)
	}
	tmp, err := os.CreateTemp("", "tash-"+name+"-*")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return "", fmt.Errorf("download Go: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

func fetchGoChecksum(version, filename string) (string, error) {
	resp, err := goHTTPClient.Get("https://go.dev/dl/?mode=json&include=all")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("go.dev returned %s", resp.Status)
	}
	var releases []goRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", err
	}
	want := "go" + version
	for _, rel := range releases {
		if rel.Version != want {
			continue
		}
		for _, file := range rel.Files {
			if file.Filename == filename {
				return strings.ToLower(file.SHA256), nil
			}
		}
	}
	return "", fmt.Errorf("checksum for %s not found", filename)
}

func verifySHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("Go checksum mismatch: got %s, want %s", got, want)
	}
	return nil
}

func extractGoArchive(path, target string) error {
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	if strings.HasSuffix(strings.ToLower(path), ".zip") {
		return extractGoZip(path, parent)
	}
	return extractGoTarGz(path, parent)
}

func extractGoZip(path, dest string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, file := range zr.File {
		name := filepath.Clean(file.Name)
		if name != "go" && !strings.HasPrefix(name, "go"+string(os.PathSeparator)) {
			continue
		}
		out := filepath.Join(dest, name)
		if !pathWithin(dest, out) {
			return fmt.Errorf("unsafe path in Go archive: %q", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		rc, err := file.Open()
		if err != nil {
			return err
		}
		mode := file.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(w, rc)
		closeErr := w.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func extractGoTarGz(path, dest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if name != "go" && !strings.HasPrefix(name, "go"+string(os.PathSeparator)) {
			continue
		}
		out := filepath.Join(dest, name)
		if !pathWithin(dest, out) {
			return fmt.Errorf("unsafe path in Go archive: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			w, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(w, tr)
			closeErr := w.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}
