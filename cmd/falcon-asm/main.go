// Command falcon-asm is a language server for GNU as x86 assembly in AT&T
// syntax. It speaks LSP over stdin/stdout; see docs/DESIGN.md. The same
// binary is the linter ("falcon-asm lint") and its rule reference
// ("falcon-asm rules").
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/engine"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/server"
)

// Set at link time with -ldflags "-X main.version=... -X main.buildTime=...".
var (
	version   = "dev"
	buildTime = "dev"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is main with its environment passed in, so tests can drive the real
// entrypoint in-process as well as through a built binary.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Subcommands; with none, the language server, which is what an
	// editor starts.
	if len(args) > 0 {
		switch args[0] {
		case "lint":
			return runLint(args[1:], stdout, stderr)
		case "rules":
			return runRules(args[1:], stdout, stderr)
		case "version":
			printVersion(stdout)
			return 0
		case "help":
			return runHelp(args[1:], stdout, stderr)
		}
		if !strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(stderr, "falcon-asm: unknown command %q\n\n", args[0])
			printUsage(stderr)
			return 2
		}
	}
	if wantsHelp(args) {
		printUsage(stdout)
		return 0
	}
	args, prof := profileArgs(args)
	var showVersion bool
	fs := serverFlagSet(&showVersion)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: falcon-asm [command] [flags]; falcon-asm --help lists them")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage(stdout)
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "falcon-asm: unexpected argument %q (the language server takes no paths)\n\n", fs.Arg(0))
		printUsage(stderr)
		return 2
	}
	if showVersion {
		printVersion(stdout)
		return 0
	}
	stop, err := prof.start()
	if err != nil {
		fmt.Fprintf(stderr, "falcon-asm: %v\n", err)
		return 2
	}
	defer stop(stderr)
	// The instruction tables take several milliseconds to parse. Loading
	// them here, beside the initialize handshake rather than before it,
	// gets the reply out at once and has the tables ready, or nearly, by
	// the time the first document is highlighted; a request that needs
	// them earlier waits on the same sync.Once. Broken embedded data
	// degrades hover and highlighting rather than stopping the server, so
	// it is reported once instead of failing.
	go func() {
		if err := isa.DataError(); err != nil {
			fmt.Fprintf(stderr, "falcon-asm: instruction data: %v\n", err)
		}
	}()
	// The hardware tables (ports, MSRs, BIOS services, CPUID: about 2.5
	// ms) load beside them, on their own goroutine so the instruction
	// tables are not delayed; both are done before the first document's
	// requests, which would otherwise parse them on the request path
	// (the first hardware hover, the first lint run).
	go func() {
		if err := hwdb.DataError(); err != nil {
			fmt.Fprintf(stderr, "falcon-asm: hardware data: %v\n", err)
		}
	}()
	srv := server.New(server.Options{
		Engine:         engine.New(engine.Config{Log: stderr}),
		TokenTypes:     server.TokenTypes,
		TokenModifiers: server.TokenModifiers,
		Name:           "falcon-asm",
		Version:        version,
		Log:            stderr,
	})
	return srv.Run(stdin, stdout)
}

// serverFlagSet defines the language server's flags. Clients append -stdio
// and -clientProcessId by convention; stdio is the only transport, and the
// client process id is not needed because exit/EOF already end the session.
func serverFlagSet(showVersion *bool) *flag.FlagSet {
	fs := flag.NewFlagSet("falcon-asm", flag.ContinueOnError)
	fs.BoolVar(showVersion, "version", false, "print version and exit")
	fs.Bool("stdio", false, "use stdin/stdout (the default and only transport)")
	fs.Int("clientProcessId", 0, "accepted for client compatibility; ignored")
	return fs
}

const usageHead = `usage: falcon-asm [command] [flags]

falcon-asm is a language server and linter for GNU as x86 assembly in AT&T
syntax (.S, .s) and the inline asm of C sources.

Commands:
  (none)            run the language server on stdin/stdout; what an editor
                    starts (see "Language server flags" below)
  lint [paths...]   lint files and directories for CI and pre-commit hooks;
                    exit 0 clean, 1 findings, 2 usage or configuration error
  rules [code...]   list the lint rules, or print rules' documentation
  version           print the version and exit
  help [command]    print this help, or a command's (help lint, help rules)

Language server flags:
`

const usageTail = `
Run "falcon-asm help lint" for the linter's flags (--severity, --changed,
--fix, --format, --jobs, --config, --compile-commands, --no-toolchain, --c)
and examples, "falcon-asm help rules" for the rule reference.
`

// printUsage writes the top-level help.
func printUsage(w io.Writer) {
	var v bool
	fs := serverFlagSet(&v)
	fs.SetOutput(w)
	fmt.Fprint(w, usageHead)
	fs.PrintDefaults()
	fmt.Fprint(w, usageTail)
}

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "falcon-asm %s (built %s)\n", version, buildTime)
}

// runHelp is "falcon-asm help [command]".
func runHelp(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}
	switch args[0] {
	case "lint":
		printLintHelp(stdout)
	case "rules":
		printRulesHelp(stdout)
	case "version", "help", "server":
		printUsage(stdout)
	default:
		fmt.Fprintf(stderr, "falcon-asm help: unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
	return 0
}

// profiles holds the hidden profiling flags. They are kept out of the flag
// set, and so out of -help, because they are a development tool (see
// docs/BENCHMARKS.md), not something an editor configuration should use.
type profiles struct {
	cpu, mem string
}

// profileArgs removes --cpuprofile and --memprofile (one or two dashes,
// "=path" or a separate argument) from args.
func profileArgs(args []string) ([]string, profiles) {
	var p profiles
	var rest []string
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		var dst *string
		switch {
		case !strings.HasPrefix(args[i], "-"):
		case name == "cpuprofile":
			dst = &p.cpu
		case name == "memprofile":
			dst = &p.mem
		}
		if dst == nil {
			rest = append(rest, args[i])
			continue
		}
		if !hasVal && i+1 < len(args) {
			i++
			val = args[i]
		}
		*dst = val
	}
	return rest, p
}

// start begins CPU profiling and returns the function that ends the
// session's profiling: the CPU profile is flushed and the heap profile
// written after the server has stopped, so both cover the whole session.
func (p profiles) start() (func(io.Writer), error) {
	var cpu *os.File
	if p.cpu != "" {
		f, err := os.Create(p.cpu)
		if err != nil {
			return nil, fmt.Errorf("cpuprofile: %w", err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("cpuprofile: %w", err)
		}
		cpu = f
	}
	return func(stderr io.Writer) {
		if cpu != nil {
			pprof.StopCPUProfile()
			if err := cpu.Close(); err != nil {
				fmt.Fprintf(stderr, "falcon-asm: cpuprofile: %v\n", err)
			}
		}
		if p.mem == "" {
			return
		}
		if err := writeHeap(p.mem); err != nil {
			fmt.Fprintf(stderr, "falcon-asm: memprofile: %v\n", err)
		}
	}, nil
}

func writeHeap(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	// A GC first, so the profile shows live data rather than garbage.
	runtime.GC()
	if err := pprof.WriteHeapProfile(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}
