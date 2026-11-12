//go:build !windows

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tash/tash/internal/config"
)

func defaultInstallDir() string {
	if config.IsTermux() {
		if prefix := os.Getenv("PREFIX"); prefix != "" {
			return filepath.Join(prefix, "bin")
		}
		return "/data/data/com.termux/files/usr/bin"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", "bin")
	}
	return filepath.Join(home, ".local", "bin")
}

func executableSuffix() string { return "" }

func validPayload(b []byte) error {
	switch runtime.GOOS {
	case "darwin":
		if len(b) < 4 {
			return errors.New("embedded payload is empty")
		}
		// Mach-O 32/64-bit and universal/fat binaries.
		if bytes.Equal(b[:4], []byte{0xfe, 0xed, 0xfa, 0xce}) ||
			bytes.Equal(b[:4], []byte{0xce, 0xfa, 0xed, 0xfe}) ||
			bytes.Equal(b[:4], []byte{0xfe, 0xed, 0xfa, 0xcf}) ||
			bytes.Equal(b[:4], []byte{0xcf, 0xfa, 0xed, 0xfe}) ||
			bytes.Equal(b[:4], []byte{0xca, 0xfe, 0xba, 0xbe}) ||
			bytes.Equal(b[:4], []byte{0xbe, 0xba, 0xfe, 0xca}) {
			return nil
		}
		return errors.New("embedded payload is not a macOS binary; rebuild the installer for darwin")
	default:
		if len(b) < 4 || !bytes.Equal(b[:4], []byte{0x7f, 'E', 'L', 'F'}) {
			return errors.New("embedded payload is not a Linux ELF binary; rebuild the installer for linux")
		}
		return nil
	}
}

func addToUserPath(dir string) (bool, error) {
	if config.IsTermux() {
		return false, nil
	}
	for _, part := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(part) == filepath.Clean(dir) {
			return false, nil
		}
	}
	files := rcFiles()
	wrote := false
	for _, path := range files {
		changed, err := appendPathBlock(path, dir)
		if err != nil {
			return wrote, err
		}
		wrote = wrote || changed
	}
	return wrote, nil
}

func rcFiles() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	shell := strings.ToLower(os.Getenv("SHELL"))
	if strings.Contains(shell, "fish") {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		return []string{filepath.Join(base, "fish", "config.fish")}
	}
	var candidates []string
	switch {
	case strings.Contains(shell, "zsh"):
		candidates = []string{".zshrc", ".zprofile"}
	case strings.Contains(shell, "bash"):
		candidates = []string{".bashrc", ".bash_profile"}
	default:
		candidates = []string{".profile"}
	}
	var out []string
	for _, name := range candidates {
		out = append(out, filepath.Join(home, name))
	}
	return out
}

func appendPathBlock(path, dir string) (bool, error) {
	const begin = "# >>> Tash installer: add tash to PATH >>>"
	const end = "# <<< Tash installer <<<"
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if bytes.Contains(existing, []byte(begin)) {
		return false, nil
	}
	line := "export PATH=" + shellQuote(dir) + ":$PATH"
	if strings.HasSuffix(filepath.ToSlash(path), "/fish/config.fish") {
		line = "fish_add_path " + shellQuote(dir)
	}
	block := begin + "\n" + line + "\n" + end + "\n"
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		block = "\n" + block
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.WriteString(block); err != nil {
		return false, err
	}
	return true, nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t'\"$`\\!*?[]#&;()|<>") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// relaunchInTerminal reopens a GUI-launched Linux installer in a terminal so
// first-run setup can run. Termux and macOS already provide a terminal context
// (the DMG ships an "Install Tash.command" launcher).
func relaunchInTerminal() (bool, error) {
	if runtime.GOOS != "linux" || config.IsTermux() {
		return false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	type terminal struct {
		name string
		args []string
	}
	candidates := []terminal{
		{"x-terminal-emulator", []string{"-e"}},
		{"gnome-terminal", []string{"--"}},
		{"konsole", []string{"-e"}},
		{"xfce4-terminal", []string{"-x"}},
		{"alacritty", []string{"-e"}},
		{"kitty", nil},
		{"xterm", []string{"-e"}},
	}
	args := os.Args[1:]
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		argv := append([]string{}, candidate.args...)
		argv = append(argv, exe)
		argv = append(argv, args...)
		cmd := exec.Command(path, argv...)
		cmd.Env = append(os.Environ(), terminalGuard+"=1")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			continue
		}
		return true, nil
	}
	return false, fmt.Errorf("no terminal emulator found; run %s from a terminal", exe)
}
