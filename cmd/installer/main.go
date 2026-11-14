// Command installer installs Tash into the user's home and then opens the
// first-run setup inside a terminal. The release scripts embed the matching
// tash executable as payload/tash_payload before compiling this program.
package main

import (
	"bufio"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tash/tash/internal/config"
	"github.com/tash/tash/internal/version"
	"golang.org/x/term"
)

//go:embed payload/*
var payloadFS embed.FS

const (
	appName       = "tash"
	payloadName   = "payload/tash_payload"
	terminalGuard = "TASH_INSTALLER_IN_TERMINAL"
)

var stdin = bufio.NewReader(os.Stdin)

type options struct {
	prefix     string
	prefixSet  bool
	yes        bool
	noPath     bool
	noLaunch   bool
	dryRun     bool
	portable   bool
	withGo     bool
	withGoSet  bool
	goVersion  string
}

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printHelp()
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "tash installer:", err)
		os.Exit(2)
	}

	// If the installer was launched from a file manager without a terminal,
	// reopen it in one so setup can actually be completed. Windows console
	// executables already get a console; the macOS DMG ships a .command file.
	if !opts.noLaunch && !term.IsTerminal(int(os.Stdin.Fd())) && os.Getenv(terminalGuard) == "" {
		if relaunched, err := relaunchInTerminal(); err != nil {
			fmt.Fprintln(os.Stderr, "tash installer: could not open a terminal:", err)
			fmt.Fprintln(os.Stderr, "Run this installer from a terminal, or re-run with --no-launch.")
		} else if relaunched {
			return
		}
	}

	code := install(opts)
	if code == 0 && !opts.noLaunch && term.IsTerminal(int(os.Stdin.Fd())) {
		if err := launchDashboard(opts); err != nil {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "Could not start `tash dashboard`:", err)
			fmt.Fprintln(os.Stderr, "Open a new terminal and run: tash dashboard")
			code = 1
		}
	}
	if term.IsTerminal(int(os.Stdin.Fd())) && os.Getenv(terminalGuard) == "" {
		fmt.Print("\nPress Enter to close this installer…")
		_, _ = stdin.ReadString('\n')
	}
	os.Exit(code)
}

func parseOptions(args []string) (options, error) {
	var opts options
	fs := flag.NewFlagSet("tash installer", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.prefix, "prefix", "", "install directory (default: platform user bin)")
	fs.BoolVar(&opts.yes, "yes", false, "install without asking for confirmation")
	fs.BoolVar(&opts.noPath, "no-path", false, "do not add the install directory to PATH")
	fs.BoolVar(&opts.noLaunch, "no-launch", false, "install only; do not start `tash dashboard`")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "show what would happen without writing files")
	fs.BoolVar(&opts.portable, "portable", false, "keep tash beside the installer; do not touch PATH")
	fs.BoolVar(&opts.withGo, "with-go", false, "also install the Go toolchain")
	fs.StringVar(&opts.goVersion, "go-version", defaultGoVersion, "Go toolchain version to install")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		return opts, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "prefix":
			opts.prefixSet = true
		case "with-go":
			opts.withGoSet = true
		}
	})
	if opts.portable {
		if !opts.prefixSet {
			if exe, err := os.Executable(); err == nil {
				opts.prefix = filepath.Dir(exe)
			} else if cwd, err := os.Getwd(); err == nil {
				opts.prefix = cwd
			}
		}
		opts.noPath = true
	}
	if opts.prefix == "" {
		opts.prefix = defaultInstallDir()
	}
	opts.prefix = config.ExpandPath(opts.prefix)
	return opts, nil
}

func install(opts options) int {
	fmt.Println()
	fmt.Println("  ████████╗ █████╗ ███████╗██╗  ██╗")
	fmt.Println("  ╚══██╔══╝██╔══██╗██╔════╝██║  ██║")
	fmt.Println("     ██║   ███████║███████╗███████║")
	fmt.Println("     ██║   ██╔══██║╚════██║██╔══██║")
	fmt.Println("     ██║   ██║  ██║███████║██║  ██║")
	fmt.Println("     ╚═╝   ╚═╝  ╚═╝╚══════╝╚═╝  ╚═╝")
	fmt.Printf("  terminal control deck  ·  installer %s\n\n", version.Version)

	payload, err := loadPayload()
	if err != nil {
		fmt.Fprintln(os.Stderr, "installer:", err)
		return 1
	}
	if err := validPayload(payload); err != nil {
		fmt.Fprintln(os.Stderr, "installer:", err)
		return 1
	}

	interactive := !opts.yes && term.IsTerminal(int(os.Stdin.Fd()))
	if interactive && !opts.prefixSet && !opts.portable {
		entered := readLine(fmt.Sprintf("  Install directory [%s]: ", opts.prefix))
		if entered != "" {
			opts.prefix = config.ExpandPath(entered)
		}
	}
	if interactive && !opts.withGoSet {
		answer := strings.ToLower(readLine("  Also install the Go toolchain? [y/N]: "))
		opts.withGo = strings.HasPrefix(answer, "y")
	}

	dest := filepath.Join(opts.prefix, appName+executableSuffix())
	fmt.Printf("  platform:  %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  install:   %s\n", dest)
	fmt.Printf("  payload:   %.1f MiB\n", float64(len(payload))/(1024*1024))
	fmt.Printf("  termux:    %v\n", config.IsTermux())
	if opts.portable {
		fmt.Println("  mode:      portable (binary stays beside the installer)")
	}
	if opts.noPath {
		fmt.Println("  PATH:      unchanged (--no-path)")
	} else if config.IsTermux() {
		fmt.Println("  PATH:      already includes $PREFIX/bin")
	} else {
		fmt.Printf("  PATH:      add %s to the user PATH\n", opts.prefix)
	}
	if opts.withGo {
		fmt.Printf("  Go:        install %s to %s\n", opts.goVersion, goToolchainDir(opts.prefix))
	} else {
		fmt.Println("  Go:        not requested")
	}
	fmt.Println()

	if opts.dryRun {
		fmt.Println("Dry run only. Nothing was written.")
		return 0
	}

	if !opts.yes {
		fmt.Println()
		fmt.Println("  SOFTWARE LICENSE AGREEMENT & TERMS OF USE")
		fmt.Println("  ─────────────────────────────────────────")
		fmt.Println("  Software: Tash")
		fmt.Println("  Developer: Nypr Studioz")
		fmt.Println("  Copyright: © 2026 Nypr. All rights reserved.")
		fmt.Println("  License Version: 1.0")
		fmt.Println()
		fmt.Println("  ---")
		fmt.Println()
		fmt.Println("  1. Acceptance of Terms")
		fmt.Println()
		fmt.Println("  By installing, accessing, launching, or using Tash, you acknowledge that you")
		fmt.Println("  have read, understood, and agreed to this Software License Agreement and its")
		fmt.Println("  Terms of Use.")
		fmt.Println()
		fmt.Println("  If you do not agree to these terms, you must not install, access, or use Tash.")
		fmt.Println()
		fmt.Println("  2. Copyright and Ownership")
		fmt.Println()
		fmt.Println("  Tash is an independently developed software project owned by Nypr Studioz.")
		fmt.Println()
		fmt.Println("  © 2026 Nypr. All rights reserved.")
		fmt.Println()
		fmt.Println("  Unless explicitly permitted in writing by Nypr Studioz, you may not:")
		fmt.Println()
		fmt.Println("  - Redistribute, resell, or commercially exploit Tash.")
		fmt.Println("  - Claim ownership of the original software.")
		fmt.Println("  - Remove or modify copyright notices.")
		fmt.Println("  - Publish modified versions as official Nypr Studioz releases.")
		fmt.Println("  - Impersonate Nypr Studioz or its developer.")
		fmt.Println("  - Copy or reproduce proprietary components outside the permissions granted by this agreement.")
		fmt.Println()
		fmt.Println("  All original source code, interface designs, documentation, branding, and proprietary")
		fmt.Println("  materials remain the intellectual property of their respective owners.")
		fmt.Println()
		fmt.Println("  3. Independent Project and No Affiliation")
		fmt.Println()
		fmt.Println("  Tash is independently developed and maintained by Nypr Studioz.")
		fmt.Println()
		fmt.Println("  Tash is NOT affiliated with, sponsored by, endorsed by, maintained by, or officially")
		fmt.Println("  connected to:")
		fmt.Println()
		fmt.Println("  - Docker")
		fmt.Println("  - Tailscale")
		fmt.Println("  - OpenAI")
		fmt.Println("  - Anthropic or Claude")
		fmt.Println("  - Pi-hole")
		fmt.Println("  - Microsoft")
		fmt.Println("  - Any other third-party company, service, platform, or integration provider.")
		fmt.Println()
		fmt.Println("  All third-party names, trademarks, logos, products, and services belong to their")
		fmt.Println("  respective owners.")
		fmt.Println()
		fmt.Println("  References to these products are solely for identification, compatibility, and")
		fmt.Println("  integration purposes.")
		fmt.Println()
		fmt.Println("  4. Third-Party Integrations")
		fmt.Println()
		fmt.Println("  Tash may provide integrations with external applications, APIs, services, and software.")
		fmt.Println()
		fmt.Println("  These integrations do not establish any partnership, sponsorship, ownership, endorsement,")
		fmt.Println("  or official relationship between Nypr Studioz and the respective providers.")
		fmt.Println()
		fmt.Println("  Nypr Studioz is not responsible for:")
		fmt.Println()
		fmt.Println("  - Third-party service outages or interruptions.")
		fmt.Println("  - API changes, restrictions, or discontinuation.")
		fmt.Println("  - External account suspensions.")
		fmt.Println("  - Third-party pricing changes.")
		fmt.Println("  - Security incidents originating from external services.")
		fmt.Println("  - Data loss caused by external platforms.")
		fmt.Println()
		fmt.Println("  Users are responsible for complying with the applicable terms and policies of")
		fmt.Println("  services they connect to Tash.")
		fmt.Println()
		fmt.Println("  5. Acceptable Use")
		fmt.Println()
		fmt.Println("  Users agree not to:")
		fmt.Println()
		fmt.Println("  - Use Tash for unlawful activities.")
		fmt.Println("  - Use the software to compromise systems or networks.")
		fmt.Println("  - Distribute malicious modifications under the Nypr Studioz name.")
		fmt.Println("  - Misrepresent the software or its capabilities.")
		fmt.Println("  - Circumvent applicable licensing restrictions.")
		fmt.Println("  - Infringe upon the intellectual property rights of others.")
		fmt.Println()
		fmt.Println("  6. Software Disclaimer")
		fmt.Println()
		fmt.Println("  Tash is provided on an \"AS IS\" and \"AS AVAILABLE\" basis, to the maximum extent")
		fmt.Println("  permitted by applicable law.")
		fmt.Println()
		fmt.Println("  Nypr Studioz does not guarantee:")
		fmt.Println()
		fmt.Println("  - Uninterrupted operation.")
		fmt.Println("  - Compatibility with every operating system or device.")
		fmt.Println("  - Permanent availability of integrations.")
		fmt.Println("  - Freedom from bugs, errors, or technical issues.")
		fmt.Println("  - Compatibility with future third-party updates.")
		fmt.Println()
		fmt.Println("  Users are responsible for maintaining appropriate backups and reviewing the")
		fmt.Println("  consequences of actions performed through the software.")
		fmt.Println()
		fmt.Println("  7. Limitation of Liability")
		fmt.Println()
		fmt.Println("  To the maximum extent permitted by applicable law, Nypr Studioz and its developer")
		fmt.Println("  shall not be liable for indirect, incidental, special, or consequential damages")
		fmt.Println("  arising from the use or inability to use Tash.")
		fmt.Println()
		fmt.Println("  Nothing in this agreement excludes liability that cannot legally be excluded.")
		fmt.Println()
		fmt.Println("  8. Updates and Modifications")
		fmt.Println()
		fmt.Println("  Nypr Studioz reserves the right to:")
		fmt.Println()
		fmt.Println("  - Update or modify Tash.")
		fmt.Println("  - Introduce, modify, or remove features.")
		fmt.Println("  - Change supported integrations.")
		fmt.Println("  - Discontinue development or distribution.")
		fmt.Println()
		fmt.Println("  Updated versions may include revised license terms. Any revised terms will be")
		fmt.Println("  presented for acceptance where required by applicable law.")
		fmt.Println()
		fmt.Println("  9. Termination")
		fmt.Println()
		fmt.Println("  Nypr Studioz may terminate the permission granted under this agreement if a user")
		fmt.Println("  materially violates its terms, subject to applicable law.")
		fmt.Println()
		fmt.Println("  Upon termination, the user must discontinue use of the software and cease any")
		fmt.Println("  distribution that is not otherwise legally permitted.")
		fmt.Println()
		fmt.Println("  10. License Grant")
		fmt.Println()
		fmt.Println("  Nypr Studioz grants the user a limited, personal, non-exclusive, non-transferable,")
		fmt.Println("  and revocable license to use Tash, subject to this agreement.")
		fmt.Println()
		fmt.Println("  This license does not transfer copyright, ownership, or intellectual property rights")
		fmt.Println("  to the user.")
		fmt.Println()
		fmt.Println("  11. Governing Law")
		fmt.Println()
		fmt.Println("  This agreement shall be interpreted in accordance with the applicable laws of Thailand,")
		fmt.Println("  subject to mandatory consumer protection and other applicable legal requirements.")
		fmt.Println()
		fmt.Println("  12. Entire Agreement")
		fmt.Println()
		fmt.Println("  This agreement represents the terms governing the user's permission to use Tash.")
		fmt.Println()
		fmt.Println("  If any provision is found unenforceable, the remaining provisions shall continue to")
		fmt.Println("  apply to the extent permitted by law.")
		fmt.Println()
		fmt.Println("  ---")
		fmt.Println()
		fmt.Println("  COPYRIGHT NOTICE")
		fmt.Println()
		fmt.Println("  Tash")
		fmt.Println()
		fmt.Println("  Developed and maintained by Nypr Studioz.")
		fmt.Println()
		fmt.Println("  © 2026 Nypr. All rights reserved.")
		fmt.Println()
		fmt.Println("  Tash is an independent software project. All third-party integrations and referenced")
		fmt.Println("  services remain the property of their respective owners.")
		fmt.Println()
		answer := strings.ToLower(strings.TrimSpace(readLine("  I have read and agree to the Software License Agreement and Terms of Use. I acknowledge the ownership, copyright, limitations, and third-party disclaimers described in this agreement. [y/N]: ")))
		if !strings.HasPrefix(answer, "y") {
			fmt.Println("Aborted. You must agree to the terms to install Tash.")
			return 1
		}
		fmt.Println()
		answer = strings.ToLower(strings.TrimSpace(readLine("Install Tash now? [Y/n]: ")))
		if strings.HasPrefix(answer, "n") {
			fmt.Println("Aborted. Nothing was installed.")
			return 1
		}
	}

	if err := os.MkdirAll(opts.prefix, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "installer: create directory:", err)
		return 1
	}
	if err := writeExecutable(dest, payload); err != nil {
		fmt.Fprintln(os.Stderr, "installer: write binary:", err)
		return 1
	}
	fmt.Println("  ✓ installed", dest)

	if !opts.noPath && !config.IsTermux() {
		added, err := addToUserPath(opts.prefix)
		if err != nil {
			fmt.Fprintln(os.Stderr, "  ! could not update PATH:", err)
			fmt.Fprintf(os.Stderr, "    Add %s to PATH manually.\n", opts.prefix)
		} else if added {
			fmt.Println("  ✓ added the install directory to your user PATH")
		} else {
			fmt.Println("  ✓ install directory is already on PATH")
		}
	}

	if opts.withGo {
		fmt.Println()
		if err := installGoToolchain(opts.prefix, opts.goVersion); err != nil {
			fmt.Fprintln(os.Stderr, "  ! Go toolchain was not installed:", err)
			fmt.Fprintln(os.Stderr, "    Tash itself is installed and ready to use.")
		}
	}

	// Windows: create Start Menu shortcut and register in Apps & Features
	if runtime.GOOS == "windows" {
		if err := createWindowsShortcuts(opts.prefix, dest); err != nil {
			fmt.Fprintln(os.Stderr, "  ! could not create Start Menu shortcut:", err)
		} else {
			fmt.Println("  ✓ created Start Menu shortcut")
		}
	}

	fmt.Println()
	if opts.noLaunch {
		fmt.Println("  Tash is installed. Start it with: tash dashboard")
	} else {
		fmt.Println("  Tash is installed. First-run setup will now open in this terminal.")
		fmt.Println("  Choose MAIN DEVICE for the first machine, or JOIN TASHNET for the rest.")
	}
	fmt.Println()
	return 0
}

func loadPayload() ([]byte, error) {
	if path := strings.TrimSpace(os.Getenv("TASH_INSTALLER_PAYLOAD")); path != "" {
		b, err := os.ReadFile(config.ExpandPath(path))
		if err != nil {
			return nil, fmt.Errorf("read TASH_INSTALLER_PAYLOAD: %w", err)
		}
		return b, nil
	}
	if b, err := payloadFS.ReadFile(payloadName); err == nil && len(b) > 0 {
		return b, nil
	}
	// Helpful for development: allow a payload beside the installer.
	if exe, err := os.Executable(); err == nil {
		for _, name := range []string{"tash_payload", "tash_payload.exe", "tash", "tash.exe"} {
			candidate := filepath.Join(filepath.Dir(exe), name)
			if b, err := os.ReadFile(candidate); err == nil && len(b) > 0 {
				return b, nil
			}
		}
	}
	return nil, errors.New("installer payload is missing; build it with scripts/build.ps1 or scripts/build.sh")
}

func writeExecutable(path string, payload []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o755); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o755)
	// Windows cannot replace an executable that is currently running, but tash
	// itself is not running here. Remove first for portability.
	_ = os.Remove(path)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func launchDashboard(opts options) error {
	dest := filepath.Join(opts.prefix, appName+executableSuffix())
	fmt.Printf("Launching `tash dashboard` from %s\n\n", dest)
	cmd := exec.Command(dest, "dashboard")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	return cmd.Run()
}

func readLine(label string) string {
	fmt.Print(label)
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

func printHelp() {
	fmt.Print(`Tash installer

  Tash-Setup -- [--prefix DIR] [--yes] [--no-path] [--no-launch] [--dry-run]
                [--portable] [--with-go] [--go-version VERSION]

  --prefix DIR       install directory (default: per-user bin directory)
  --yes              do not ask for confirmation
  --no-path          do not modify the user PATH
  --no-launch        install only; do not start first-run setup
  --dry-run          print the plan without writing anything
  --portable         keep tash beside the installer; no PATH changes
  --with-go          also install the Go toolchain
  --go-version VER   Go version to install (default: ` + defaultGoVersion + `)

The installer writes one self-contained binary named ` + "`tash`" + ` and then
opens ` + "`tash dashboard`" + ` in the terminal. First run creates the Tashnet
main device or joins an existing network.

Run without flags for an interactive install where you can change the
install directory and choose whether to install Go.
`)
}

func createWindowsShortcuts(prefix, dest string) error {
	// Find icon.png - try embedded first, then relative to installer
	iconPath := ""
	if b, err := payloadFS.ReadFile("payload/icon.png"); err == nil && len(b) > 0 {
		iconPath = filepath.Join(prefix, "icon.png")
		if err := os.WriteFile(iconPath, b, 0o644); err != nil {
			iconPath = ""
		}
	} else {
		// Try to find icon relative to installer
		if exe, err := os.Executable(); err == nil {
			for _, name := range []string{"icon.png"} {
				candidate := filepath.Join(filepath.Dir(exe), name)
				if _, err := os.Stat(candidate); err == nil {
					iconPath = filepath.Join(prefix, "icon.png")
					if err := copyFile(candidate, iconPath); err != nil {
						iconPath = ""
					}
					break
				}
			}
		}
	}

	// Create Start Menu shortcut
	startMenu := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs")
	tashDir := filepath.Join(startMenu, "Tash")
	if err := os.MkdirAll(tashDir, 0o755); err != nil {
		return err
	}

	shortcutPath := filepath.Join(tashDir, "Tash.lnk")
	psScript := fmt.Sprintf(`$WshShell = New-Object -ComObject WScript.Shell
$Shortcut = $WshShell.CreateShortcut("%s")
$Shortcut.TargetPath = "%s"
$Shortcut.Arguments = "dashboard"
$Shortcut.WorkingDirectory = "%s"
$Shortcut.Description = "Tash - Terminal Control Deck"
if (Test-Path "%s") {
	$Shortcut.IconLocation = "%s"
}
$Shortcut.Save()`, shortcutPath, dest, prefix, iconPath, iconPath)

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	if err := cmd.Run(); err != nil {
		return err
	}

	// Also create uninstall shortcut
	uninstallScript := filepath.Join(prefix, "uninstall.ps1")
	uninstallContent := fmt.Sprintf(`# Tash Uninstaller
Write-Host "Uninstalling Tash..."
Remove-Item -LiteralPath "%s" -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath "%s" -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath "%s" -Force -ErrorAction SilentlyContinue
# Remove from PATH (user scope)
$oldPath = [Environment]::GetEnvironmentVariable("Path", "User")
$newPath = ($oldPath -split ";" | Where-Object { $_ -ne "%s" }) -join ";"
[Environment]::SetEnvironmentVariable("Path", $newPath, "User")
Write-Host "Tash uninstalled."
`, dest, filepath.Join(prefix, "icon.png"), shortcutPath, prefix)

	if err := os.WriteFile(uninstallScript, []byte(uninstallContent), 0o644); err == nil {
		uninstallShortcut := filepath.Join(tashDir, "Uninstall Tash.lnk")
		psScript2 := fmt.Sprintf(`$WshShell = New-Object -ComObject WScript.Shell
$Shortcut = $WshShell.CreateShortcut("%s")
$Shortcut.TargetPath = "powershell.exe"
$Shortcut.Arguments = "-NoProfile -ExecutionPolicy Bypass -File \"%s\""
$Shortcut.WorkingDirectory = "%s"
$Shortcut.Description = "Uninstall Tash"
$Shortcut.Save()`, uninstallShortcut, uninstallScript, prefix)
		_ = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript2).Run()
	}

	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
