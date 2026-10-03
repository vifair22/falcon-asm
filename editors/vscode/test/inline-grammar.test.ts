import * as fs from "fs";
import * as path from "path";
import { fileURLToPath } from "url";
import { describe, expect, it } from "vitest";
import { Tokenizer, matchSelector, type Grammar, type Rule } from "./tm";

const here = path.dirname(fileURLToPath(import.meta.url));
const extRoot = path.resolve(here, "..");
// Real inline asm from airies-boot; read-only. The test skips when the
// sibling checkout is absent so the suite still runs elsewhere.
const xstatePath = path.resolve(extRoot, "../../../airies-boot/falcon-core/src/crypto/xstate.c");

function readJson<T>(rel: string): T {
  return JSON.parse(fs.readFileSync(path.join(extRoot, rel), "utf8")) as T;
}

interface GrammarContribution {
  language?: string;
  scopeName: string;
  path: string;
  injectTo?: string[];
}

const pkg = readJson<{ contributes: { grammars: GrammarContribution[] } }>("package.json");
const contribution = pkg.contributes.grammars.find((g) => g.injectTo !== undefined);
if (contribution === undefined) {
  throw new Error("package.json contributes no injection grammar");
}
const inline = readJson<Grammar & { injectionSelector: string }>(contribution.path);

// A stand-in for VS Code's bundled C grammar, reduced to what matters here:
// its own asm rule (so a test fails if the injection does not pre-empt it),
// strings, comments, parens and identifiers, and the two repository entries
// the injection includes (comments, evaluation_context). Scope names follow
// the real grammar.
function hostGrammar(scopeName: "source.c" | "source.cpp"): Grammar {
  const sfx = scopeName === "source.c" ? "c" : "cpp";
  const str: Rule = {
    name: `string.quoted.double.${sfx}`,
    begin: '"',
    end: '"',
    patterns: [{ name: `constant.character.escape.${sfx}`, match: "\\\\." }],
  };
  return {
    scopeName,
    patterns: [
      { include: "#comments" },
      {
        name: `meta.asm.${sfx}`,
        begin: "(\\b(?:__asm__|asm)\\b)\\s*((?:volatile)?)\\s*\\(",
        end: "\\)",
        patterns: [{ ...str, contentName: `meta.embedded.assembly.${sfx}` }],
      },
      str,
      { begin: "\\(", end: "\\)", patterns: [{ include: "$self" }] },
      { name: `constant.numeric.${sfx}`, match: "\\b\\d+[uUlL]*\\b" },
      { name: `variable.other.${sfx}`, match: "\\b[A-Za-z_]\\w*\\b" },
    ],
    repository: {
      comments: {
        patterns: [
          { name: `comment.block.${sfx}`, begin: "/\\*", end: "\\*/" },
          { name: `comment.line.double-slash.${sfx}`, match: "//.*" },
        ],
      },
      evaluation_context: { patterns: [{ include: "$self" }] },
    },
  };
}

function tokenizer(host: "source.c" | "source.cpp" = "source.c"): Tokenizer {
  // The C grammar is always registered: the injection includes source.c#...
  // even in C++ documents, as VS Code allows.
  return new Tokenizer(hostGrammar(host), { registry: [hostGrammar("source.c")], injections: [inline] });
}

// Scopes of the first character of the nth `needle` on line `lineNo`, after
// tokenizing every earlier line so multi-line state is real.
function scopesOf(lines: string[], lineNo: number, needle: string, nth = 0, host?: "source.c" | "source.cpp"): string[] {
  const tok = tokenizer(host);
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

const on = (line: string, needle: string, nth = 0): string[] => scopesOf([line], 0, needle, nth);

const MNEMONIC = "keyword.other.mnemonic.falcon-asm";
const REGISTER = "variable.other.register.falcon-asm";
const OPERAND = "variable.parameter.operand.falcon-asm";
const CONSTRAINT = "string.quoted.double.constraint.falcon-asm";
const TEMPLATE = "meta.embedded.assembly.falcon-asm";
const STATEMENT = "meta.asm.falcon-asm";

function walk(g: Grammar, visit: (r: Rule, where: string) => void): void {
  const go = (r: Rule, where: string): void => {
    visit(r, where);
    (r.patterns ?? []).forEach((p, i) => go(p, `${where}.patterns[${i}]`));
  };
  g.patterns.forEach((p, i) => go(p, `patterns[${i}]`));
  for (const [k, r] of Object.entries(g.repository)) {
    go(r, `repository.${k}`);
  }
}

describe("injection grammar structure", () => {
  it("is contributed as an injection into C and C++", () => {
    expect(contribution.language).toBeUndefined();
    expect(contribution.injectTo).toEqual(["source.c", "source.cpp"]);
    expect(inline.scopeName).toBe(contribution.scopeName);
  });

  it("injects with L: priority, outside strings, comments and its own statements", () => {
    const alts = inline.injectionSelector.split(",").map((s) => s.trim());
    expect(alts).toEqual([
      "L:source.c -string -comment -meta.asm.falcon-asm",
      "L:source.cpp -string -comment -meta.asm.falcon-asm",
    ]);
  });

  it("has regexes that compile as JS and avoid Oniguruma-only syntax", () => {
    const onigOnly = /\\[hHAZzGK]|\(\?>|\(\?[imx-]+[):]|[*+?}]\+|\[\[:/;
    let n = 0;
    walk(inline, (r, where) => {
      for (const key of ["match", "begin", "end"] as const) {
        const src = r[key];
        if (src !== undefined) {
          n++;
          expect(() => new RegExp(src, "dg"), `${where}.${key}`).not.toThrow();
          expect(onigOnly.test(src.replace(/\\\\/g, "")), `${where}.${key}: ${src}`).toBe(false);
        }
      }
    });
    expect(n).toBeGreaterThan(10);
  });

  it("names every scope under .falcon-asm and includes only known host entries", () => {
    const external = new Set<string>();
    walk(inline, (r) => {
      for (const n of [r.name, r.contentName]) {
        if (n !== undefined) {
          expect(n, n).toMatch(/^[a-z0-9-]+(\.[a-z0-9-]+)*\.falcon-asm$/);
        }
      }
      for (const caps of [r.captures, r.beginCaptures, r.endCaptures]) {
        for (const c of Object.values(caps ?? {})) {
          expect(c.name, c.name).toMatch(/^[a-z0-9-]+(\.[a-z0-9-]+)*\.falcon-asm$/);
        }
      }
      if (r.include !== undefined && !r.include.startsWith("#")) {
        external.add(r.include);
      }
    });
    // The host entries must exist in the bundled C grammar; checked against
    // the real file below when it is installed.
    expect([...external].sort()).toEqual(["source.c#comments", "source.c#evaluation_context"]);
  });

  it("is pure ASCII", () => {
    const raw = fs.readFileSync(path.join(extRoot, contribution.path), "utf8");
    expect(raw).toMatch(/^[\x09\x0a\x20-\x7e]*$/);
  });
});

describe("matchSelector", () => {
  it.each([
    ["L:source.c -string", ["source.c", "meta.block.c"], true],
    ["L:source.c -string", ["source.c", "string.quoted.double.c"], false],
    ["L:source.c -string", ["source.cpp"], false],
    ["source.c meta.asm", ["source.c", "meta.block.c", "meta.asm.c"], true],
    ["meta.asm source.c", ["source.c", "meta.asm.c"], false],
    ["L:source.c, L:source.cpp", ["source.cpp"], true],
  ])("%s on %j -> %s", (sel, scopes, want) => {
    expect(matchSelector(sel, scopes)).toBe(want);
  });
});

describe("xstate.c inline asm", () => {
  const present = fs.existsSync(xstatePath);
  const lines = present ? fs.readFileSync(xstatePath, "utf8").split("\n") : [];
  const lineOf = (text: string): number => {
    const i = lines.findIndex((l) => l.includes(text));
    if (i < 0) {
      throw new Error(`xstate.c line not found: ${text}`);
    }
    return i;
  };

  it.skipIf(!present)("splits the cr4 read into template and operands", () => {
    const n = lineOf('__asm__ volatile ("mov %%cr4, %0" : "=r"(v));');
    const at = (needle: string, nth = 0): string[] => scopesOf(lines, n, needle, nth);
    expect(at("__asm__")).toContain("storage.type.asm.falcon-asm");
    // The injection pre-empts the host's own asm rule rather than nesting in it.
    expect(at("__asm__")).not.toContain("meta.asm.c");
    expect(at("volatile")).toContain("storage.modifier.falcon-asm");
    expect(at("mov")).toContain(MNEMONIC);
    expect(at("mov")).toContain(TEMPLATE);
    expect(at("%%cr4")).toContain(REGISTER);
    expect(at("%%cr4")).toContain("punctuation.definition.variable.falcon-asm");
    expect(at("cr4")).toContain(REGISTER);
    expect(at("%0")).toContain(OPERAND);
    expect(at(":")).toContain("meta.asm.operands.falcon-asm");
    expect(at("=r")).toContain(CONSTRAINT);
    expect(at("r\"(")).not.toContain(MNEMONIC);
    // The C operand goes through the host's evaluation_context.
    expect(at("v)")).toContain("variable.other.c");
    expect(at(";")).not.toContain(STATEMENT);
  });

  it.skipIf(!present)("keeps clobbers as plain strings", () => {
    const n = lineOf('__asm__ volatile ("mov %0, %%cr4" : : "r"(v) : "memory");');
    expect(scopesOf(lines, n, "memory")).toContain(CONSTRAINT);
    expect(scopesOf(lines, n, "memory")).not.toContain(MNEMONIC);
    expect(scopesOf(lines, n, ":", 1)).toContain("punctuation.separator.colon.falcon-asm");
    expect(scopesOf(lines, n, "%0")).toContain(OPERAND);
  });

  it.skipIf(!present)("paints a bare instruction and register constraints", () => {
    const n = lineOf('__asm__ volatile ("xgetbv"');
    expect(scopesOf(lines, n, "xgetbv")).toContain(MNEMONIC);
    expect(scopesOf(lines, n, '"=a"')).toContain(CONSTRAINT);
    expect(scopesOf(lines, n, "0u")).toContain("constant.numeric.c");
  });

  it.skipIf(!present)("carries the operand list across lines and closes after it", () => {
    const n = lineOf('__asm__ volatile ("xsetbv"');
    expect(scopesOf(lines, n, "xsetbv")).toContain(MNEMONIC);
    expect(scopesOf(lines, n + 1, '"d"')).toContain(CONSTRAINT);
    expect(scopesOf(lines, n + 1, "uint32_t")).toContain("meta.asm.operands.falcon-asm");
    expect(scopesOf(lines, n + 1, ">>")).toContain(STATEMENT);
    expect(scopesOf(lines, n + 1, ";")).not.toContain(STATEMENT);
  });

  it.skipIf(!present)("does not take clobbered register names for registers or mnemonics", () => {
    const n = lineOf('__asm__ volatile ("vzeroall" : : : "memory",');
    expect(scopesOf(lines, n, "vzeroall")).toContain(MNEMONIC);
    for (const [dl, reg] of [[1, "xmm0"], [2, "xmm8"], [3, "xmm15"]] as const) {
      const s = scopesOf(lines, n + dl, reg);
      expect(s, reg).toContain(CONSTRAINT);
      expect(s, reg).not.toContain(REGISTER);
      expect(s, reg).not.toContain(MNEMONIC);
    }
  });

  it.skipIf(!present)("tokenizes the whole file back to top level", () => {
    const tok = tokenizer();
    let asmLines = 0;
    for (const l of lines) {
      const s = tok.tokenizeLine(l);
      const at = l.indexOf("__asm__");
      if (at >= 0) {
        asmLines++;
        expect(s[at], l).toContain(STATEMENT);
      }
    }
    expect(asmLines).toBeGreaterThan(4);
    expect(tok.depth()).toBe(1);
  });
});

describe("inline asm constructs", () => {
  it("paints labels, instructions after \\n\\t, and local label references", () => {
    const line = 'asm volatile("1:\\n\\trdrand %0\\n\\tjnc 1b" : "=r"(x));';
    expect(on(line, "1:")).toContain("entity.name.label.falcon-asm");
    expect(on(line, "\\n")).toContain("constant.character.escape.falcon-asm");
    expect(on(line, "rdrand")).toContain(MNEMONIC);
    expect(on(line, "jnc")).toContain(MNEMONIC);
    expect(on(line, "1b")).toContain("entity.name.label.reference.falcon-asm");
  });

  it("paints the instruction that follows a label on the same statement", () => {
    expect(on('asm("retry: pause; jmp retry")', "retry")).toContain("entity.name.function.falcon-asm");
    expect(on('asm("retry: pause; jmp retry")', "pause")).toContain(MNEMONIC);
    expect(on('asm("retry: pause; jmp retry")', "jmp")).toContain(MNEMONIC);
    expect(on('asm("retry: pause; jmp retry")', "retry", 1)).not.toContain(MNEMONIC);
  });

  it.each([
    ['asm("lock; cmpxchg %2, %1")', "lock", MNEMONIC],
    ['asm("lock; cmpxchg %2, %1")', "cmpxchg", MNEMONIC],
    ['asm("rep movsb")', "rep", "storage.modifier.prefix.falcon-asm"],
    ['asm("rep movsb")', "movsb", MNEMONIC],
    ['asm("repne scasb")', "scasb", MNEMONIC],
    ['asm("\\tnop")', "nop", MNEMONIC],
    ['asm(".byte 0x0f, 0x01, 0xd0")', ".byte", "keyword.other.directive.falcon-asm"],
    ['asm(".byte 0x0f, 0x01, 0xd0")', "0x01", "constant.numeric.hex.falcon-asm"],
    ['asm("movl $0x10, %%eax")', "$", "constant.other.immediate.falcon-asm"],
    ['asm("movl $0x10, %%eax")', "0x10", "constant.numeric.hex.falcon-asm"],
    ['asm("movl $16, %%eax")', "16", "constant.numeric.decimal.falcon-asm"],
    ['asm("mov %%fs:0x28, %0" : "=r"(c))', "fs", REGISTER],
    ['asm("mov %%fs:0x28, %0" : "=r"(c))', "0x28", "constant.numeric.hex.falcon-asm"],
    ['asm("fld %%st(1)")', "(1", REGISTER],
    ['asm("movzbl %b1, %k0" : "=r"(a) : "q"(b))', "%b1", OPERAND],
    ['asm("movzbl %b1, %k0" : "=r"(a) : "q"(b))', "%k0", OPERAND],
    ['asm("cli; hlt")', "hlt", MNEMONIC],
    ['asm("mov %eax, %ebx")', "%ebx", REGISTER],
    ['asm("jmp 1f%=")', "%=", OPERAND],
    ['__asm__ __volatile__ ("pause")', "pause", MNEMONIC],
    ['__asm__ __volatile__ ("pause")', "__volatile__", "storage.modifier.falcon-asm"],
    ['__asm ("pause")', "pause", MNEMONIC],
    ['asm inline ("pause")', "pause", MNEMONIC],
    ['asm goto ("jmp %l[out]" : : : : out);', "%l[out]", OPERAND],
    ['asm goto ("jmp %l[out]" : : : : out);', "goto", "storage.modifier.falcon-asm"],
  ])("%s: %s", (line, needle, scope) => {
    expect(on(line, needle)).toContain(scope);
  });

  it("links %[name] in the template to [name] in the operands", () => {
    const line = 'asm("mov %[val], %%rax" :: [val] "r"(x) : "rax");';
    expect(on(line, "%[val]")).toContain(OPERAND);
    expect(on(line, "val]", 1)).toContain(OPERAND);
    expect(on(line, ":", 1)).toContain("punctuation.separator.colon.falcon-asm");
    expect(on(line, '"rax"')).toContain(CONSTRAINT);
  });

  it("does not paint a mnemonic after a tab inside a statement", () => {
    expect(on('asm("mov\\tfoo, %0" : "=r"(x))', "foo")).not.toContain(MNEMONIC);
  });

  it("handles string concatenation across lines", () => {
    const src = [
      "asm volatile(",
      '    "mov %1, %0\\n\\t"',
      '    "add $1, %0"',
      '    : "=r"(y) : "r"(x));',
      "int after;",
    ];
    expect(scopesOf(src, 1, "mov")).toContain(MNEMONIC);
    expect(scopesOf(src, 2, "add")).toContain(MNEMONIC);
    expect(scopesOf(src, 3, "y")).toContain("variable.other.c");
    expect(scopesOf(src, 4, "after")).not.toContain(STATEMENT);
  });

  it.each([
    ['/* asm("nop") */', "nop"],
    ['// asm("nop")', "nop"],
    ['printf("asm(nop)");', "nop"],
  ])("leaves %s alone", (line, needle) => {
    expect(on(line, needle)).not.toContain(MNEMONIC);
    expect(on(line, needle)).not.toContain(STATEMENT);
  });

  it("applies in C++ documents too", () => {
    const s = scopesOf(['asm volatile("pause" ::: "memory");'], 0, "pause", 0, "source.cpp");
    expect(s).toContain(MNEMONIC);
    expect(s).not.toContain("meta.asm.cpp");
  });
});

// The injection relies on two facts about VS Code's bundled C/C++ grammars:
// their asm rule starts matching at the keyword itself (so an L: injection
// matching at the same position wins the tie), and source.c has the
// repository entries the injection includes. Checked against the real files
// when an install is found; VSCODE_CPP_SYNTAXES points at another one.
const bundledDirs = [
  process.env.VSCODE_CPP_SYNTAXES,
  "/usr/share/code/resources/app/extensions/cpp/syntaxes",
  "/opt/vscode/resources/app/extensions/cpp/syntaxes",
  "/opt/visual-studio-code/resources/app/extensions/cpp/syntaxes",
  "/usr/lib/code/extensions/cpp/syntaxes",
].filter((d): d is string => d !== undefined && d !== "");
const bundled = bundledDirs.find((d) => fs.existsSync(path.join(d, "c.tmLanguage.json")));

describe("bundled VS Code C/C++ grammars", () => {
  interface Raw {
    name?: string;
    begin?: string;
    patterns?: Raw[];
    repository?: Record<string, Raw>;
  }
  const load = (f: string): Raw => JSON.parse(fs.readFileSync(path.join(bundled ?? "", f), "utf8")) as Raw;
  const findAsm = (g: Raw, name: string): Raw | undefined => {
    let hit: Raw | undefined;
    const go = (r: Raw): void => {
      if (r.name === name && r.begin !== undefined) {
        hit ??= r;
      }
      (r.patterns ?? []).forEach(go);
      Object.values(r.repository ?? {}).forEach(go);
    };
    go(g);
    return hit;
  };

  it.skipIf(bundled === undefined).each([
    ["c.tmLanguage.json", "meta.asm.c"],
    ["cpp.tmLanguage.json", "meta.asm.cpp"],
  ])("%s starts its asm rule (%s) at the keyword", (file, scope) => {
    const rule = findAsm(load(file), scope);
    expect(rule, scope).toBeDefined();
    expect(rule?.begin?.startsWith("(\\b(?:__asm__|asm)\\b)")).toBe(true);
  });

  it.skipIf(bundled === undefined)("source.c has the repository entries the injection includes", () => {
    const repo = load("c.tmLanguage.json").repository ?? {};
    expect(repo.comments).toBeDefined();
    expect(repo.evaluation_context).toBeDefined();
  });
});
