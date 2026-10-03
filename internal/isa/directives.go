package isa

import "strings"

// directiveList is the GNU as directive table. Summaries describe GAS
// behaviour on x86 ELF targets specifically, because several directives
// (.align, .word, .int, .long) mean different things on other targets.
var directiveList = []Directive{
	// Code size and syntax
	{".code16", "Assemble following code for 16-bit mode (real mode or a 16-bit segment)", ".code16"},
	{".code32", "Assemble following code for 32-bit mode", ".code32"},
	{".code64", "Assemble following code for 64-bit long mode", ".code64"},
	{".code16gcc", "Assemble 16-bit code from gcc output: like .code16, but call, ret, enter, leave, push, pop, pusha, popa, pushf and popf default to 32-bit operand size", ".code16gcc"},
	{".intel_syntax", "Switch to Intel operand order and syntax; with noprefix, registers are written without '%'", ".intel_syntax [prefix|noprefix]"},
	{".att_syntax", "Switch to AT&T syntax (the default); with noprefix, registers are written without '%'", ".att_syntax [prefix|noprefix]"},
	{".arch", "Restrict accepted instructions to a CPU or ISA extension (e.g. i386, i686, .sse2, .nosse); later uses add to or remove from the set", ".arch cpu_type[, [no]jumps]"},

	// Sections
	{".text", "Switch to the .text section (or a numbered subsection of it)", ".text [subsection]"},
	{".data", "Switch to the .data section (or a numbered subsection of it)", ".data [subsection]"},
	{".bss", "Switch to the .bss section (or a numbered subsection of it)", ".bss [subsection]"},
	{".section", "Switch to the named section, creating it if needed; flags a=alloc w=write x=exec M=merge S=strings G=group T=TLS ...", ".section name[, \"flags\"[, @type[, entsize|group]]]"},
	{".previous", "Swap the current section and subsection with the previously selected one", ".previous"},
	{".pushsection", "Push the current section onto the section stack and switch to the named section", ".pushsection name[, subsection][, \"flags\"[, @type]]"},
	{".popsection", "Pop the section stack, returning to the section that was current at the matching .pushsection", ".popsection"},
	{".subsection", "Switch to a numbered subsection of the current section", ".subsection n"},

	// Symbols
	{".globl", "Make a symbol visible to the linker (global binding)", ".globl symbol[, symbol...]"},
	{".global", "Same as .globl: make a symbol visible to the linker", ".global symbol[, symbol...]"},
	{".local", "ELF: give a symbol local binding, so it is not visible outside this object", ".local symbol[, symbol...]"},
	{".weak", "Give a symbol weak binding, creating it if needed; a strong definition elsewhere overrides it", ".weak symbol[, symbol...]"},
	{".weakref", "Define alias as a weak reference to target; target is not made weak unless referenced directly", ".weakref alias, target"},
	{".hidden", "ELF: set symbol visibility to hidden (not exported from the shared object or executable)", ".hidden symbol[, symbol...]"},
	{".protected", "ELF: set symbol visibility to protected (exported, but references within the component bind locally)", ".protected symbol[, symbol...]"},
	{".internal", "ELF: set symbol visibility to internal", ".internal symbol[, symbol...]"},
	{".extern", "Accepted for compatibility and ignored: GAS treats every undefined symbol as external", ".extern symbol"},
	{".type", "ELF: set a symbol's type, e.g. @function, @object, @notype, @tls_object, @gnu_indirect_function", ".type symbol, @type"},
	{".size", "ELF: set a symbol's size, usually .-symbol at the end of a function or object", ".size symbol, expression"},
	{".comm", "Declare a common symbol of the given size, merged by the linker with others of the same name", ".comm symbol, length[, alignment]"},
	{".lcomm", "Reserve length bytes in .bss for a local symbol", ".lcomm symbol, length[, alignment]"},
	{".symver", "Bind a symbol to a version node for shared library symbol versioning", ".symver name, name2@nodename"},

	// Symbol assignment
	{".equ", "Set symbol to the value of expression; may be redefined later", ".equ symbol, expression"},
	{".set", "Set symbol to the value of expression; same as .equ and may be redefined later", ".set symbol, expression"},
	{".equiv", "Like .equ, but an error if the symbol is already defined", ".equiv symbol, expression"},
	{".eqv", "Like .equ, but the expression is re-evaluated at each use of the symbol", ".eqv symbol, expression"},

	// Alignment and location
	{".align", "Pad to an alignment boundary; on x86 ELF the argument is a byte count (on some other targets a power of two). Pads with nops in code sections", ".align alignment[, [fill][, max]]"},
	{".balign", "Pad to a boundary given in bytes, optionally with a fill byte and a maximum number of bytes to skip", ".balign alignment[, [fill][, max]]"},
	{".balignw", "Like .balign, with a 2-byte fill pattern", ".balignw alignment[, [fill][, max]]"},
	{".balignl", "Like .balign, with a 4-byte fill pattern", ".balignl alignment[, [fill][, max]]"},
	{".p2align", "Pad to a boundary of 2**n bytes, optionally with a fill byte and a maximum number of bytes to skip", ".p2align n[, [fill][, max]]"},
	{".p2alignw", "Like .p2align, with a 2-byte fill pattern", ".p2alignw n[, [fill][, max]]"},
	{".p2alignl", "Like .p2align, with a 4-byte fill pattern", ".p2alignl n[, [fill][, max]]"},
	{".org", "Advance the location counter to new-lc within the current section, filling the gap; cannot move backwards", ".org new-lc[, fill]"},
	{".fill", "Emit repeat copies of a size-byte value (size defaults to 1, at most 8; value defaults to 0)", ".fill repeat[, size[, value]]"},
	{".skip", "Emit size bytes, each of value fill (default 0); same as .space", ".skip size[, fill]"},
	{".space", "Emit size bytes, each of value fill (default 0)", ".space size[, fill]"},
	{".zero", "Emit size zero bytes", ".zero size"},
	{".nops", "x86: emit size bytes of nop instructions, each at most control bytes long", ".nops size[, control]"},

	// Data
	{".byte", "Emit each expression as one byte", ".byte expression[, expression...]"},
	{".word", "Emit each expression as a 2-byte value on x86", ".word expression[, expression...]"},
	{".short", "Emit each expression as a 2-byte value", ".short expression[, expression...]"},
	{".hword", "Emit each expression as a 2-byte value on x86 (half the target word)", ".hword expression[, expression...]"},
	{".value", "x86: emit each expression as a 2-byte value", ".value expression[, expression...]"},
	{".2byte", "Emit each expression as an unaligned 2-byte value", ".2byte expression[, expression...]"},
	{".int", "Emit each expression as a 4-byte value on x86", ".int expression[, expression...]"},
	{".long", "Emit each expression as a 4-byte value on x86", ".long expression[, expression...]"},
	{".4byte", "Emit each expression as an unaligned 4-byte value", ".4byte expression[, expression...]"},
	{".quad", "Emit each expression as an 8-byte value", ".quad expression[, expression...]"},
	{".8byte", "Emit each expression as an unaligned 8-byte value", ".8byte expression[, expression...]"},
	{".octa", "Emit each bignum as a 16-byte value", ".octa bignum[, bignum...]"},
	{".sleb128", "Emit each expression as a signed LEB128 variable-length number", ".sleb128 expression[, expression...]"},
	{".uleb128", "Emit each expression as an unsigned LEB128 variable-length number", ".uleb128 expression[, expression...]"},
	{".float", "Emit each value as a 4-byte IEEE single", ".float value[, value...]"},
	{".single", "Emit each value as a 4-byte IEEE single", ".single value[, value...]"},
	{".double", "Emit each value as an 8-byte IEEE double", ".double value[, value...]"},
	{".tfloat", "x86: emit each value as a 10-byte x87 extended-precision float", ".tfloat value[, value...]"},
	{".ascii", "Emit each string with no trailing NUL", ".ascii \"string\"[, \"string\"...]"},
	{".asciz", "Emit each string followed by a NUL byte", ".asciz \"string\"[, \"string\"...]"},
	{".string", "Emit each string followed by a NUL byte (same as .asciz on x86)", ".string \"string\"[, \"string\"...]"},
	{".incbin", "Include the raw bytes of a file, optionally skipping a prefix and limiting the count", ".incbin \"file\"[, skip[, count]]"},

	// Macros and repetition
	{".macro", "Define a macro; parameters are referenced as \\name in the body, which ends at .endm", ".macro name [param[=default][:req|:vararg], ...]"},
	{".endm", "End the current macro definition", ".endm"},
	{".exitm", "Exit the current macro expansion early", ".exitm"},
	{".purgem", "Remove a macro definition so the name can be redefined", ".purgem name"},
	{".altmacro", "Enable alternate macro mode (local names, % expression evaluation, <string> quoting)", ".altmacro"},
	{".noaltmacro", "Disable alternate macro mode", ".noaltmacro"},
	{".rept", "Repeat the lines up to .endr count times", ".rept count"},
	{".irp", "Repeat the lines up to .endr once per value, with \\symbol replaced by each value", ".irp symbol, value[, value...]"},
	{".irpc", "Repeat the lines up to .endr once per character, with \\symbol replaced by each character", ".irpc symbol, characters"},
	{".endr", "End a .rept, .irp or .irpc block", ".endr"},

	// Conditional assembly
	{".if", "Assemble the following block if the absolute expression is non-zero", ".if expression"},
	{".ifdef", "Assemble the following block if the symbol is defined", ".ifdef symbol"},
	{".ifndef", "Assemble the following block if the symbol is not defined", ".ifndef symbol"},
	{".ifnotdef", "Same as .ifndef", ".ifnotdef symbol"},
	{".ifeq", "Assemble the following block if the expression is zero", ".ifeq expression"},
	{".ifne", "Assemble the following block if the expression is non-zero (same as .if)", ".ifne expression"},
	{".ifgt", "Assemble the following block if the expression is greater than zero", ".ifgt expression"},
	{".ifge", "Assemble the following block if the expression is greater than or equal to zero", ".ifge expression"},
	{".iflt", "Assemble the following block if the expression is less than zero", ".iflt expression"},
	{".ifle", "Assemble the following block if the expression is less than or equal to zero", ".ifle expression"},
	{".ifb", "Assemble the following block if the operand is blank (useful for optional macro arguments)", ".ifb text"},
	{".ifnb", "Assemble the following block if the operand is not blank", ".ifnb text"},
	{".ifc", "Assemble the following block if the two strings are identical", ".ifc string1, string2"},
	{".ifnc", "Assemble the following block if the two strings differ", ".ifnc string1, string2"},
	{".ifeqs", "Assemble the following block if the two quoted strings are identical", ".ifeqs \"string1\", \"string2\""},
	{".ifnes", "Assemble the following block if the two quoted strings differ", ".ifnes \"string1\", \"string2\""},
	{".else", "Assemble the following block if the preceding conditions were false", ".else"},
	{".elseif", "Shorthand for .else followed by a nested .if, without a separate .endif", ".elseif expression"},
	{".endif", "End a conditional block", ".endif"},

	// Files and diagnostics
	{".include", "Insert the contents of a source file at this point (searched in -I directories)", ".include \"file\""},
	{".file", "Record the source file name (and, with a number, define a DWARF file entry for .loc)", ".file [fileno] \"filename\""},
	{".loc", "Add a DWARF line-number row for the following instruction", ".loc fileno lineno [column] [options]"},
	{".ident", "ELF: append a string to the .comment section", ".ident \"string\""},
	{".error", "Report an error with the given message and fail the assembly", ".error \"string\""},
	{".err", "Report an error and fail the assembly (typically inside a conditional)", ".err"},
	{".warning", "Report a warning with the given message", ".warning \"string\""},
	{".print", "Print a string on standard output during assembly", ".print \"string\""},
	{".end", "Mark the end of the source; anything after it is ignored", ".end"},
	{".list", "Enable listing output (counter-based, balanced with .nolist)", ".list"},
	{".nolist", "Disable listing output", ".nolist"},

	// Call frame information. Each must sit between .cfi_startproc and
	// .cfi_endproc; together they build .eh_frame (or .debug_frame) entries.
	{".cfi_startproc", "Begin a call-frame description for a function and emit its initial CFI instructions", ".cfi_startproc [simple]"},
	{".cfi_endproc", "End the current call-frame description and emit its FDE", ".cfi_endproc"},
	{".cfi_sections", "Choose which sections receive CFI: .eh_frame, .debug_frame or both", ".cfi_sections section_list"},
	{".cfi_def_cfa", "Define the CFA as register plus offset", ".cfi_def_cfa register, offset"},
	{".cfi_def_cfa_offset", "Set a new CFA offset, keeping the CFA register", ".cfi_def_cfa_offset offset"},
	{".cfi_def_cfa_register", "Set a new CFA register, keeping the offset", ".cfi_def_cfa_register register"},
	{".cfi_adjust_cfa_offset", "Add to the current CFA offset (e.g. after a push or a stack adjustment)", ".cfi_adjust_cfa_offset delta"},
	{".cfi_offset", "Record that register's previous value is saved at offset from the CFA", ".cfi_offset register, offset"},
	{".cfi_rel_offset", "Like .cfi_offset, but offset is relative to the current CFA register, not the CFA", ".cfi_rel_offset register, offset"},
	{".cfi_register", "Record that register1's previous value is now held in register2", ".cfi_register register1, register2"},
	{".cfi_restore", "Restore register's rule to what it was at .cfi_startproc", ".cfi_restore register"},
	{".cfi_undefined", "Record that register's previous value cannot be recovered", ".cfi_undefined register"},
	{".cfi_same_value", "Record that register still holds its value from the previous frame", ".cfi_same_value register"},
	{".cfi_remember_state", "Push the current set of CFI rules onto a stack", ".cfi_remember_state"},
	{".cfi_restore_state", "Pop the CFI rules saved by .cfi_remember_state", ".cfi_restore_state"},
	{".cfi_return_column", "Set the column (register) holding the return address", ".cfi_return_column register"},
	{".cfi_signal_frame", "Mark the current function as a signal trampoline frame", ".cfi_signal_frame"},
	{".cfi_personality", "Set the personality routine and its pointer encoding for exception handling", ".cfi_personality encoding[, exp]"},
	{".cfi_lsda", "Set the language-specific data area pointer and its encoding", ".cfi_lsda encoding[, exp]"},
	{".cfi_escape", "Emit raw bytes into the current CFI as DWARF call-frame instructions", ".cfi_escape expression[, ...]"},
}

var directives = func() map[string]*Directive {
	m := make(map[string]*Directive, len(directiveList))
	for i := range directiveList {
		m[directiveList[i].Name] = &directiveList[i]
	}
	return m
}()

// LookupDirective finds a directive by name, with or without the leading
// '.', ignoring case (GAS matches directive names case-insensitively).
func LookupDirective(name string) (*Directive, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if !strings.HasPrefix(n, ".") {
		n = "." + n
	}
	d, ok := directives[n]
	return d, ok
}
