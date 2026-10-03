// Command falcon-asm is a language server for GNU as x86 assembly in AT&T
// syntax. It speaks LSP over stdin/stdout; see docs/DESIGN.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

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
	// Broken embedded data degrades hover and highlighting rather than
	// stopping the server, so it is reported once instead of failing.
	if err := isa.DataError(); err != nil {
		fmt.Fprintf(stderr, "falcon-asm: instruction data: %v\n", err)
	}
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
