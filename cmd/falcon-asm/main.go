// Command falcon-asm is a language server for GNU as x86 assembly in AT&T
// syntax. It speaks LSP over stdin/stdout; see docs/DESIGN.md.
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
	args, prof := profileArgs(args)
	fs := flag.NewFlagSet("falcon-asm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	// Clients append these by convention; stdio is the only transport, and
	// the client process id is not needed because exit/EOF already end the
	// session.
	fs.Bool("stdio", false, "use stdin/stdout (the default and only transport)")
	fs.Int("clientProcessId", 0, "accepted for client compatibility; ignored")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "falcon-asm %s (built %s)\n", version, buildTime)
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
