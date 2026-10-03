import * as fs from "fs";
import * as path from "path";
import { fileURLToPath } from "url";
import { beforeAll, describe, expect, it } from "vitest";
import { CONFIG_SECTION, LANGUAGE_ID } from "../src/server-command";
import { Tokenizer, type Grammar, type Rule } from "./tm";

const here = path.dirname(fileURLToPath(import.meta.url));
const extRoot = path.resolve(here, "..");
const fixtures = path.resolve(extRoot, "../../testdata/falcon-bios");

function readJson<T>(rel: string): T {
  return JSON.parse(fs.readFileSync(path.join(extRoot, rel), "utf8")) as T;
}

interface PackageJson {
  name: string;
  main: string;
  activationEvents: string[];
  contributes: {
    languages: { id: string; extensions: string[]; configuration: string }[];
    grammars: { language: string; scopeName: string; path: string }[];
    semanticTokenScopes: { language: string; scopes: Record<string, string[]> }[];
    configurationDefaults: Record<string, Record<string, unknown>>;
    configuration: { properties: Record<string, { default?: unknown; enum?: string[] }> };
    commands: { command: string }[];
  };
}

interface LanguageConfiguration {
  comments: { lineComment: string; blockComment: [string, string] };
  brackets: [string, string][];
  autoClosingPairs: { open: string; close: string }[];
  wordPattern: string;
}

const pkg = readJson<PackageJson>("package.json");
const grammarPath = pkg.contributes.grammars[0].path;
const grammar = readJson<Grammar & { name: string }>(grammarPath);
const langConf = readJson<LanguageConfiguration>(pkg.contributes.languages[0].configuration);

// Every regex source in the grammar, with a label saying where it lives.
function allRegexes(): { where: string; src: string }[] {
  const out: { where: string; src: string }[] = [];
  const visit = (r: Rule, where: string): void => {
    for (const key of ["match", "begin", "end"] as const) {
      const src = r[key];
      if (src !== undefined) {
        out.push({ where: `${where}.${key}`, src });
      }
    }
    (r.patterns ?? []).forEach((p, i) => visit(p, `${where}.patterns[${i}]`));
  };
  grammar.patterns.forEach((p, i) => visit(p, `patterns[${i}]`));
  for (const [k, r] of Object.entries(grammar.repository)) {
    visit(r, `repository.${k}`);
  }
  return out;
}

// Scope names in the grammar, including capture names.
function allScopeNames(): string[] {
  const out: string[] = [];
  const visit = (r: Rule): void => {
    if (r.name !== undefined) {
      out.push(r.name);
    }
    for (const caps of [r.captures, r.beginCaptures, r.endCaptures]) {
      for (const c of Object.values(caps ?? {})) {
        if (c.name !== undefined) {
          out.push(c.name);
        }
      }
    }
    (r.patterns ?? []).forEach(visit);
  };
  grammar.patterns.forEach(visit);
  Object.values(grammar.repository).forEach(visit);
  return out;
}

describe("grammar structure", () => {
  it("is wired to the contributed language", () => {
    expect(grammar.scopeName).toBe("source.falcon-asm");
    expect(pkg.contributes.grammars[0]).toMatchObject({
      language: LANGUAGE_ID,
      scopeName: grammar.scopeName,
    });
  });

  it("has a regex for every rule and they all compile as JS RegExp", () => {
    const regexes = allRegexes();
    expect(regexes.length).toBeGreaterThan(10);
    for (const { where, src } of regexes) {
      expect(() => new RegExp(src, "dg"), where).not.toThrow();
    }
  });

  it("uses no Oniguruma-only syntax, so JS RegExp results are representative", () => {
    // \h \H \A \Z \z \G, atomic groups, possessive quantifiers, inline flags,
    // and POSIX bracket classes all parse differently (or not at all) in JS.
    const onigOnly = /\\[hHAZzGK]|\(\?>|\(\?[imx-]+[):]|[*+?}]\+|\[\[:/;
    for (const { where, src } of allRegexes()) {
      // Drop escaped backslashes first so "\\\\h" (a literal backslash then h) is not flagged.
      const stripped = src.replace(/\\\\/g, "");
      expect(onigOnly.test(stripped), `${where}: ${src}`).toBe(false);
    }
  });

  it("resolves every include and names every scope under .falcon-asm", () => {
    const tok = new Tokenizer(grammar);
    expect(() => tok.flatten(grammar.patterns)).not.toThrow();
    const names = allScopeNames();
    expect(names.length).toBeGreaterThan(20);
    for (const n of names) {
      expect(n, n).toMatch(/^[a-z0-9-]+(\.[a-z0-9-]+)*\.falcon-asm$/);
    }
  });

  it("is pure ASCII", () => {
    const raw = fs.readFileSync(path.join(extRoot, grammarPath), "utf8");
    expect(raw).toMatch(/^[\x09\x0a\x20-\x7e]*$/);
  });
});

describe("language configuration", () => {
  it("declares GAS comments", () => {
    expect(langConf.comments).toEqual({ lineComment: "#", blockComment: ["/*", "*/"] });
  });

  it("closes brackets and quotes", () => {
    expect(langConf.brackets).toContainEqual(["(", ")"]);
    const opens = langConf.autoClosingPairs.map((p) => p.open);
    expect(opens).toEqual(expect.arrayContaining(["(", "[", "{", '"', "'"]));
  });

  it("has a word pattern that keeps register and directive sigils", () => {
    const re = new RegExp(langConf.wordPattern, "g");
    expect("movw $0x2401, %ax".match(re)).toEqual(["movw", "0x2401", "%ax"]);
    expect("\t.code16".match(re)).toEqual([".code16"]);
    expect("jnz 1f".match(re)).toEqual(["jnz", "1f"]);
  });
});

describe("package.json contributions", () => {
  const legend = [
    "keyword", "modifier", "variable", "number", "label", "macro",
    "comment", "string", "operator", "type", "parameter",
  ];

  it("maps every semantic token type in the server legend", () => {
    const entry = pkg.contributes.semanticTokenScopes.find((s) => s.language === LANGUAGE_ID);
    expect(entry).toBeDefined();
    for (const t of legend) {
      const scopes = entry?.scopes[t];
      expect(scopes, t).toBeDefined();
      expect(scopes?.length, t).toBeGreaterThan(0);
    }
    // Every selector key is either a legend type or a modifier selector over one.
    for (const key of Object.keys(entry?.scopes ?? {})) {
      const [type, ...mods] = key.split(".");
      expect(type === "*" || legend.includes(type), key).toBe(true);
      for (const m of mods) {
        expect(["declaration", "defaultLibrary", "deprecated"], key).toContain(m);
      }
    }
  });

  it("turns semantic highlighting on for the language", () => {
    expect(pkg.contributes.configurationDefaults[`[${LANGUAGE_ID}]`]).toEqual({
      "editor.semanticHighlighting.enabled": true,
    });
  });

  it("declares the settings the client reads", () => {
    const props = pkg.contributes.configuration.properties;
    expect(props[`${CONFIG_SECTION}.serverPath`]?.default).toBe("falcon-asm");
    expect(props[`${CONFIG_SECTION}.trace.server`]?.enum).toEqual(["off", "messages", "verbose"]);
  });

  it("contributes the language for .S and .s, the restart command, and an activation event", () => {
    const lang = pkg.contributes.languages.find((l) => l.id === LANGUAGE_ID);
    expect(lang?.extensions).toEqual([".S", ".s"]);
    expect(pkg.contributes.commands.map((c) => c.command)).toContain("falcon-asm.restartServer");
    expect(pkg.activationEvents).toContain(`onLanguage:${LANGUAGE_ID}`);
    expect(pkg.main).toBe("./out/extension.js");
  });
});

// Scopes of the first character of `needle` on `line`, after tokenizing every
// line before it so multi-line state (block comments) is real.
function scopesOf(lines: string[], lineNo: number, needle: string, nth = 0): string[] {
  const tok = new Tokenizer(grammar);
  let result: string[][] = [];
  for (let i = 0; i <= lineNo; i++) {
    result = tok.tokenizeLine(lines[i]);
  }
  const line = lines[lineNo];
  let at = -1;
  for (let k = 0; k <= nth; k++) {
    at = line.indexOf(needle, at + 1);
  }
  if (at < 0) {
    throw new Error(`"${needle}" not on line ${lineNo + 1}: ${line}`);
  }
  return result[at];
}

function scopesInline(line: string, needle: string, nth = 0): string[] {
  return scopesOf([line], 0, needle, nth);
}

describe("tokenizing pmode.S", () => {
  let lines: string[] = [];
  const lineOf = (text: string): number => {
    const i = lines.findIndex((l) => l.includes(text));
    if (i < 0) {
      throw new Error(`fixture line not found: ${text}`);
    }
    return i;
  };

  beforeAll(() => {
    lines = fs.readFileSync(path.join(fixtures, "pmode.S"), "utf8").split("\n");
  });

  it("paints the header block comment as a comment on every line", () => {
    expect(scopesOf(lines, 0, "/*")).toContain("comment.block.falcon-asm");
    // A line in the middle of the block, which on its own would look like code.
    const mid = lineOf("Entered in 16-bit real mode");
    expect(scopesOf(lines, mid, "Entered")).toContain("comment.block.falcon-asm");
    expect(scopesOf(lines, mid, "Entered")).not.toContain("keyword.other.mnemonic.falcon-asm");
  });

  it("reads #include as a cpp directive with a string path, not a comment", () => {
    const n = lineOf("#include <start/biosdefs.h>");
    const hash = scopesOf(lines, n, "#include");
    expect(hash).toContain("meta.preprocessor.include.falcon-asm");
    expect(hash).toContain("keyword.control.directive.include.falcon-asm");
    expect(hash).not.toContain("comment.line.number-sign.falcon-asm");
    expect(scopesOf(lines, n, "<start")).toContain("string.quoted.other.include.falcon-asm");
  });

  it("paints directives, mnemonics, registers, immediates and numbers", () => {
    expect(scopesOf(lines, lineOf("\t.code16"), ".code16")).toContain("keyword.other.directive.falcon-asm");
    const movw = lineOf("movw    $0x2401, %ax");
    expect(scopesOf(lines, movw, "movw")).toContain("keyword.other.mnemonic.falcon-asm");
    expect(scopesOf(lines, movw, "$")).toContain("constant.other.immediate.falcon-asm");
    expect(scopesOf(lines, movw, "0x2401")).toContain("constant.numeric.hex.falcon-asm");
    const ax = scopesOf(lines, movw, "%ax");
    expect(ax).toContain("variable.other.register.falcon-asm");
    expect(ax).toContain("punctuation.definition.variable.falcon-asm");
    expect(scopesOf(lines, movw, "ax")).toContain("variable.other.register.falcon-asm");
  });

  it("keeps a trailing block comment out of the operands", () => {
    const n = lineOf("jnz     1f");
    expect(scopesOf(lines, n, "1f")).toContain("entity.name.label.reference.falcon-asm");
    expect(scopesOf(lines, n, "already")).toContain("comment.block.falcon-asm");
    expect(scopesOf(lines, n, "already")).not.toContain("keyword.other.mnemonic.falcon-asm");
  });

  it("paints label definitions, named and numeric", () => {
    expect(scopesOf(lines, lineOf("pmode_enter:"), "pmode_enter")).toContain("entity.name.function.falcon-asm");
    expect(scopesOf(lines, lines.indexOf("1:"), "1")).toContain("entity.name.label.falcon-asm");
  });

  it("paints strings with escapes", () => {
    const n = lineOf(".asciz  \"falcon-bios");
    expect(scopesOf(lines, n, ".asciz")).toContain("keyword.other.directive.falcon-asm");
    expect(scopesOf(lines, n, "falcon-bios")).toContain("string.quoted.double.falcon-asm");
    expect(scopesOf(lines, n, "\\r")).toContain("constant.character.escape.falcon-asm");
  });

  it("tokenizes the whole file without the tokenizer stalling and ends at top level", () => {
    const tok = new Tokenizer(grammar);
    for (const l of lines) {
      tok.tokenizeLine(l);
    }
    expect(tok.depth()).toBe(1);
  });
});

describe("tokenizing other constructs", () => {
  it.each([
    "#if", "#ifdef FOO", "#ifndef FOO", "#elif X", "#elifdef X", "#elifndef X", "#else",
    "#endif", "#error nope", "#warning hm", "#pragma once", "#line 4", "  #  undef FOO",
    "#include_next <x.h>",
  ])("%s is a cpp directive", (line) => {
    const s = scopesInline(line, "#");
    expect(s.some((x) => x.startsWith("meta.preprocessor"))).toBe(true);
    expect(s.some((x) => x.startsWith("keyword.control.directive"))).toBe(true);
    expect(s).not.toContain("comment.line.number-sign.falcon-asm");
  });

  it.each([
    "# just a comment",
    "#includes are not a directive word",
    "#definitely a comment",
    "\tmovl %eax, %ebx # trailing",
  ])("%s gets a '#' line comment", (line) => {
    expect(scopesInline(line, "#")).toContain("comment.line.number-sign.falcon-asm");
  });

  it("paints a #define name and its body, across a continuation", () => {
    const src = ["#define SEL_C32 0x08 \\", "\t+ 4", "\tmovw %ax, %bx"];
    expect(scopesOf(src, 0, "SEL_C32")).toContain("entity.name.function.preprocessor.falcon-asm");
    expect(scopesOf(src, 0, "0x08")).toContain("constant.numeric.hex.falcon-asm");
    expect(scopesOf(src, 1, "4")).toContain("meta.preprocessor.macro.falcon-asm");
    // The continuation ended; the next line is code again.
    expect(scopesOf(src, 2, "movw")).toContain("keyword.other.mnemonic.falcon-asm");
    expect(scopesOf(src, 2, "movw")).not.toContain("meta.preprocessor.macro.falcon-asm");
  });

  it("paints // comments", () => {
    expect(scopesInline("\tnop // why", "//")).toContain("comment.line.double-slash.falcon-asm");
  });

  it("paints the mnemonic after a numeric label and after ';'", () => {
    const line = "1:\taddb    $'0', %al ; incb %al";
    expect(scopesInline(line, "1")).toContain("entity.name.label.falcon-asm");
    expect(scopesInline(line, "addb")).toContain("keyword.other.mnemonic.falcon-asm");
    expect(scopesInline(line, "'0'")).toContain("constant.character.falcon-asm");
    expect(scopesInline(line, "incb")).toContain("keyword.other.mnemonic.falcon-asm");
  });

  it("paints a directive after a label", () => {
    expect(scopesInline('cleared: .asciz "x"', ".asciz")).toContain("keyword.other.directive.falcon-asm");
  });

  it("accepts GAS one-quote character constants", () => {
    expect(scopesInline("\tmovb $'\\r, %al", "'")).toContain("constant.character.falcon-asm");
    expect(scopesInline("\tmovb $'\\r, %al", "%al")).toContain("variable.other.register.falcon-asm");
  });

  it.each([
    ["0b1011", "constant.numeric.binary.falcon-asm"],
    ["0b", "entity.name.label.reference.falcon-asm"],
    ["017", "constant.numeric.octal.falcon-asm"],
    ["42", "constant.numeric.decimal.falcon-asm"],
    ["0XfF", "constant.numeric.hex.falcon-asm"],
  ])("number %s", (num, scope) => {
    expect(scopesInline(`\t.long ${num}`, num)).toContain(scope);
  });

  it("does not take a symbol assignment for a mnemonic", () => {
    expect(scopesInline("size = 4", "size")).not.toContain("keyword.other.mnemonic.falcon-asm");
  });

  it("does not let digits inside a register become a number", () => {
    expect(scopesInline("\tmovaps %xmm10, %xmm1", "10")).toContain("variable.other.register.falcon-asm");
    expect(scopesInline("\tmovaps %xmm10, %xmm1", "10")).not.toContain("constant.numeric.decimal.falcon-asm");
  });

  it("ends an unterminated string at end of line", () => {
    const src = ['\t.ascii "oops', "\tnop"];
    expect(scopesOf(src, 1, "nop")).toContain("keyword.other.mnemonic.falcon-asm");
  });
});

describe("other fixtures", () => {
  it("tokenizes every falcon-bios file back to top level", () => {
    const files = fs.readdirSync(fixtures).filter((f) => f.endsWith(".S"));
    expect(files.length).toBeGreaterThan(0);
    for (const f of files) {
      const tok = new Tokenizer(grammar);
      for (const l of fs.readFileSync(path.join(fixtures, f), "utf8").split("\n")) {
        tok.tokenizeLine(l);
      }
      expect(tok.depth(), f).toBe(1);
    }
  });
});
