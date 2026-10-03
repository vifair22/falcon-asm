// Package all registers every lint rule. Import it for its side effect
// wherever rules must exist: the engine, the CLI and the tests that check
// docs/RULES.md against the registry. A new rule package is added here,
// and only here.
package all

import (
	// Section 1: encoding and operand validity.
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/encoding"
	// Section 2: mode, CPU and ISA target.
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/target"
	// Section 2: CPU profiles.
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/cpu"
	// Sections 2, 5 and 7: descriptor tables, boot protocol, hardware
	// constants; also registers the hardware tables loader.
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/hw"
	// Section 5: boot protocol checks on the dataflow (mode-switch order,
	// loader sector count, real-mode reach).
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/boot"
	// Section 3: dataflow (and the CPUID guard check of section 2).
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/flowrules"
	// Section 4: calling conventions and interrupt handlers (and the
	// stack frame alignment check of section 6).
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/cc"
	// Section 6: memory layout and size.
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/layout"
	// Section 8: performance lints.
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/perf"
	// Section 9: inline asm in C.
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/inlinec"
	// Section 10: symbols and structure.
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/symbols"
)
