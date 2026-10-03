package server

// Semantic token legend from docs/DESIGN.md. The wire carries indices into
// these slices, so the constants and the slices must stay in the same order;
// TestLegendOrder pins that. The semtok package emits these constants and the
// server advertises the slices, so both sides share one definition.

// Token type indices.
const (
	TokenKeyword   uint32 = iota // mnemonic
	TokenModifier                // prefix
	TokenVariable                // register
	TokenNumber                  // numeric literal
	TokenLabel                   // label definition and reference
	TokenMacro                   // cpp directive, macro use
	TokenComment                 // comment
	TokenString                  // string literal
	TokenOperator                // AT&T size suffix
	TokenType                    // assembler directive
	TokenParameter               // immediate '$'
)

// Token modifier bits. The wire value is a bit set, so these are masks, not
// indices.
const (
	ModDeclaration    uint32 = 1 << iota // label definitions
	ModDefaultLibrary                    // privileged instruction (CPL 0)
	ModDeprecated                        // phase 2: invalid in the current mode
)

// TokenTypes is the advertised token type legend, indexed by the Token*
// constants.
var TokenTypes = []string{
	"keyword",
	"modifier",
	"variable",
	"number",
	"label",
	"macro",
	"comment",
	"string",
	"operator",
	"type",
	"parameter",
}

// TokenModifiers is the advertised modifier legend; bit i of a token's
// modifier set refers to TokenModifiers[i].
var TokenModifiers = []string{
	"declaration",
	"defaultLibrary",
	"deprecated",
}
