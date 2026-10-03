package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

const rulesUsage = `usage: falcon-asm rules [--markdown] [code...]

With no code, lists every rule. With codes, prints their documentation.
--markdown prints docs/RULES.md (make rules-doc regenerates the file).

Flags:
`

func runRules(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("falcon-asm rules", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, rulesUsage)
		fs.PrintDefaults()
	}
	markdown := fs.Bool("markdown", false, "print the rule reference as Markdown (docs/RULES.md)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *markdown {
		fmt.Fprint(stdout, lint.Markdown())
		return 0
	}
	if fs.NArg() == 0 {
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "CODE\tSECTION\tDEFAULT\tSUMMARY")
		for _, r := range lint.Rules() {
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", r.Code, r.Section, r.Default, r.Doc.Summary)
		}
		if err := tw.Flush(); err != nil {
			fmt.Fprintf(stderr, "falcon-asm rules: %v\n", err)
			return 2
		}
		return 0
	}
	status := 0
	for i, code := range fs.Args() {
		r, ok := lint.Lookup(code)
		if !ok {
			fmt.Fprintf(stderr, "falcon-asm rules: unknown rule %q (falcon-asm rules lists them)\n", code)
			status = 2
			continue
		}
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		fmt.Fprint(stdout, r.Text())
	}
	return status
}
