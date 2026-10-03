// A deliberately tiny TextMate interpreter for tests.
//
// It is not vscode-textmate: no backreferences in end patterns, no
// applyEndPatternLast, no while rules, and scope selectors only in the
// simple form our injection grammar uses. It covers exactly the subset our
// grammars use (match, begin/end, captures, contentName, includes of
// #repository entries, $self, other grammars and their repository entries,
// and L: injections) and follows the same scan rule: at each position the
// earliest match wins, ties go to the end pattern, then to injections, then
// to the earliest rule in the list. That is enough to check that real
// source lines get the scopes we expect, without pulling oniguruma into the
// dev dependencies.
//
// Using JS RegExp in place of Oniguruma is sound only because the grammars
// avoid Oniguruma-only syntax; grammar.test.ts and inline-grammar.test.ts
// enforce that.

export interface Capture {
  name?: string;
}

export interface Rule {
  name?: string;
  contentName?: string;
  comment?: string;
  match?: string;
  begin?: string;
  end?: string;
  include?: string;
  captures?: Record<string, Capture>;
  beginCaptures?: Record<string, Capture>;
  endCaptures?: Record<string, Capture>;
  patterns?: Rule[];
}

export interface Grammar {
  scopeName: string;
  patterns: Rule[];
  repository: Record<string, Rule>;
  injectionSelector?: string;
}

interface Frame {
  rule: Rule;
  /** Grammar whose repository the rule's own `#name` includes resolve against. */
  grammar: Grammar;
  /** Scopes of the rule's content (name plus contentName). */
  scopes: string[];
  /** Scopes the begin and end matches get (name only). */
  outer: string[];
}

interface Leaf {
  rule: Rule;
  grammar: Grammar;
}

export interface TokenizerOptions {
  /** Other grammars reachable through `scope` and `scope#name` includes. */
  registry?: Grammar[];
  /** Injection grammars; each applies where its injectionSelector matches the scope stack. */
  injections?: Grammar[];
}

function scopeMatches(selector: string, scope: string): boolean {
  return scope === selector || scope.startsWith(selector + ".");
}

/**
 * Matches the selector subset injection grammars use: comma-separated
 * alternatives, each an optional L:/R: priority, a space-separated path of
 * scopes that must appear in order, and `-scope` exclusions. Priority is not
 * modelled: every injection is treated as L:, which is the only kind we
 * ship.
 */
export function matchSelector(selector: string, scopes: string[]): boolean {
  return selector.split(",").some((alt) => {
    const words = alt.trim().replace(/^[LR]:/, "").trim().split(/\s+/);
    const include = words.filter((w) => !w.startsWith("-"));
    const exclude = words.filter((w) => w.startsWith("-")).map((w) => w.slice(1));
    if (exclude.some((x) => scopes.some((s) => scopeMatches(x, s)))) {
      return false;
    }
    let at = 0;
    for (const want of include) {
      while (at < scopes.length && !scopeMatches(want, scopes[at])) {
        at++;
      }
      if (at === scopes.length) {
        return false;
      }
      at++;
    }
    return true;
  });
}

export class Tokenizer {
  private readonly cache = new Map<string, RegExp>();
  private readonly registry = new Map<string, Grammar>();
  private readonly injections: Grammar[];
  private stack: Frame[];

  constructor(private readonly grammar: Grammar, opts: TokenizerOptions = {}) {
    for (const g of [grammar, ...(opts.registry ?? []), ...(opts.injections ?? [])]) {
      this.registry.set(g.scopeName, g);
    }
    this.injections = opts.injections ?? [];
    const top = [grammar.scopeName];
    this.stack = [{ rule: { patterns: grammar.patterns }, grammar, scopes: top, outer: top }];
  }

  private lookup(scopeName: string): Grammar {
    const g = this.registry.get(scopeName);
    if (g === undefined) {
      throw new Error(`include of unknown grammar ${scopeName}`);
    }
    return g;
  }

  /** Every leaf rule (one with match or begin) reachable from `rules`, in priority order. */
  flatten(rules: Rule[] | undefined, grammar: Grammar = this.grammar): Rule[] {
    return this.leaves(rules, grammar, new Set()).map((l) => l.rule);
  }

  private leaves(rules: Rule[] | undefined, grammar: Grammar, seen: Set<string>): Leaf[] {
    const out: Leaf[] = [];
    for (const r of rules ?? []) {
      if (r.include !== undefined) {
        let target: Rule | undefined;
        let owner = grammar;
        if (r.include === "$self") {
          target = { patterns: grammar.patterns };
        } else if (r.include.startsWith("#")) {
          target = grammar.repository[r.include.slice(1)];
        } else {
          const [scope, key] = r.include.split("#");
          owner = this.lookup(scope);
          target = key === undefined ? { patterns: owner.patterns } : owner.repository[key];
        }
        if (target === undefined) {
          throw new Error(`include of missing repository entry ${r.include}`);
        }
        const id = r.include.startsWith("#") ? `${grammar.scopeName}${r.include}` : r.include;
        if (seen.has(id)) {
          continue;
        }
        const next = new Set(seen);
        next.add(id);
        out.push(...this.leaves([target], owner, next));
      } else if (r.match !== undefined || r.begin !== undefined) {
        out.push({ rule: r, grammar });
      } else {
        out.push(...this.leaves(r.patterns, grammar, seen));
      }
    }
    return out;
  }

  /** Injected rules active for a scope stack, ahead of the frame's own rules (L: priority). */
  private injected(scopes: string[]): Leaf[] {
    const out: Leaf[] = [];
    for (const g of this.injections) {
      if (g.injectionSelector !== undefined && matchSelector(g.injectionSelector, scopes)) {
        out.push(...this.leaves(g.patterns, g, new Set()));
      }
    }
    return out;
  }

  private re(src: string): RegExp {
    let re = this.cache.get(src);
    if (re === undefined) {
      re = new RegExp(src, "dg");
      this.cache.set(src, re);
    }
    return re;
  }

  private exec(src: string, line: string, pos: number): RegExpExecArray | null {
    const re = this.re(src);
    re.lastIndex = pos;
    return re.exec(line);
  }

  /** Tokenizes one line, carrying begin/end state across calls. Returns the scopes of every character. */
  tokenizeLine(line: string): string[][] {
    const scopes: string[][] = Array.from({ length: line.length }, () => []);
    const paint = (from: number, to: number, s: string[]): void => {
      for (let i = from; i < to; i++) {
        scopes[i] = [...s];
      }
    };
    const add = (from: number, to: number, name: string | undefined): void => {
      if (name === undefined) {
        return;
      }
      for (let i = from; i < to; i++) {
        scopes[i].push(name);
      }
    };
    const paintCaptures = (m: RegExpExecArray, caps: Record<string, Capture> | undefined): void => {
      for (const [k, c] of Object.entries(caps ?? {})) {
        const span = m.indices?.[Number(k)];
        if (span !== undefined) {
          add(span[0], span[1], c.name);
        }
      }
    };

    let pos = 0;
    for (let guard = 0; pos <= line.length; guard++) {
      if (guard > 10 * (line.length + 10)) {
        throw new Error(`tokenizer did not advance on line: ${line}`);
      }
      const frame = this.stack[this.stack.length - 1];
      let best: { m: RegExpExecArray; leaf: Leaf | null } | undefined;
      if (frame.rule.end !== undefined) {
        const m = this.exec(frame.rule.end, line, pos);
        if (m !== null) {
          best = { m, leaf: null };
        }
      }
      const candidates = [...this.injected(frame.scopes), ...this.leaves(frame.rule.patterns, frame.grammar, new Set())];
      for (const leaf of candidates) {
        const m = this.exec((leaf.rule.match ?? leaf.rule.begin) as string, line, pos);
        if (m !== null && (best === undefined || m.index < best.m.index)) {
          best = { m, leaf };
        }
      }
      if (best === undefined) {
        paint(pos, line.length, frame.scopes);
        break;
      }
      const { m, leaf } = best;
      const start = m.index;
      const stop = start + m[0].length;
      paint(pos, start, frame.scopes);

      if (leaf === null) {
        // End of the current begin/end region.
        paint(start, stop, frame.outer);
        paintCaptures(m, frame.rule.endCaptures ?? frame.rule.captures);
        this.stack.pop();
        pos = stop;
        if (pos >= line.length && start === stop && this.stack[this.stack.length - 1].rule.end === undefined) {
          // A zero-width end at end of line (the `$` of a cpp directive):
          // nothing is left to scan at the outer level.
          break;
        }
        continue;
      }

      const rule = leaf.rule;
      const inner = rule.name !== undefined ? [...frame.scopes, rule.name] : [...frame.scopes];
      paint(start, stop, inner);
      if (rule.match !== undefined) {
        paintCaptures(m, rule.captures);
        // A zero-width match must not stall the scan.
        pos = stop === start ? stop + 1 : stop;
      } else {
        paintCaptures(m, rule.beginCaptures ?? rule.captures);
        const content = rule.contentName !== undefined ? [...inner, rule.contentName] : inner;
        this.stack.push({ rule, grammar: leaf.grammar, scopes: content, outer: inner });
        pos = stop;
      }
      if (pos >= line.length && this.stack[this.stack.length - 1].rule.end === undefined) {
        break;
      }
    }
    return scopes;
  }

  /** Depth of the begin/end stack, 1 meaning top level. */
  depth(): number {
    return this.stack.length;
  }
}
