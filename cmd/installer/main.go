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
		answer := strings.ToLower(strings.TrimSpace(readLine("Install Tash now? [Y/n]: ")))
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
