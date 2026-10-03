// A deliberately tiny TextMate interpreter for tests.
//
// It is not vscode-textmate: no injections, no backreferences in end
// patterns, no applyEndPatternLast, no while rules. It covers exactly the
// subset our grammar uses (match, begin/end, captures, include of
// #repository entries) and follows the same scan rule: at each position the
// earliest match wins, ties go to the end pattern and then to the earliest
// rule in the list. That is enough to check that real source lines get the
// scopes we expect, without pulling oniguruma into the dev dependencies.
//
// Using JS RegExp in place of Oniguruma is sound only because the grammar
// avoids Oniguruma-only syntax; grammar.test.ts enforces that.

export interface Capture {
  name?: string;
}

export interface Rule {
  name?: string;
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
}

interface Frame {
  rule: Rule;
  scopes: string[];
}

export class Tokenizer {
  private readonly cache = new Map<string, RegExp>();
  private stack: Frame[];

  constructor(private readonly grammar: Grammar) {
    this.stack = [{ rule: { patterns: grammar.patterns }, scopes: [grammar.scopeName] }];
  }

  /** Every leaf rule (one with match or begin) reachable from `rules`, in priority order. */
  flatten(rules: Rule[] | undefined, seen: Set<string> = new Set()): Rule[] {
    const out: Rule[] = [];
    for (const r of rules ?? []) {
      if (r.include !== undefined) {
        if (!r.include.startsWith("#")) {
          throw new Error(`unsupported include ${r.include}`);
        }
        const key = r.include.slice(1);
        const target = this.grammar.repository[key];
        if (target === undefined) {
          throw new Error(`include of missing repository entry ${r.include}`);
        }
        if (seen.has(key)) {
          continue;
        }
        const next = new Set(seen);
        next.add(key);
        out.push(...this.flatten([target], next));
      } else if (r.match !== undefined || r.begin !== undefined) {
        out.push(r);
      } else {
        out.push(...this.flatten(r.patterns, seen));
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
      let best: { m: RegExpExecArray; rule: Rule | null } | undefined;
      if (frame.rule.end !== undefined) {
        const m = this.exec(frame.rule.end, line, pos);
        if (m !== null) {
          best = { m, rule: null };
        }
      }
      for (const rule of this.flatten(frame.rule.patterns)) {
        const m = this.exec((rule.match ?? rule.begin) as string, line, pos);
        if (m !== null && (best === undefined || m.index < best.m.index)) {
          best = { m, rule };
        }
      }
      if (best === undefined) {
        paint(pos, line.length, frame.scopes);
        break;
      }
      const { m, rule } = best;
      const start = m.index;
      const stop = start + m[0].length;
      paint(pos, start, frame.scopes);

      if (rule === null) {
        // End of the current begin/end region.
        paint(start, stop, frame.scopes);
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

      const inner = rule.name !== undefined ? [...frame.scopes, rule.name] : [...frame.scopes];
      paint(start, stop, inner);
      if (rule.match !== undefined) {
        paintCaptures(m, rule.captures);
        // A zero-width match must not stall the scan.
        pos = stop === start ? stop + 1 : stop;
      } else {
        paintCaptures(m, rule.beginCaptures ?? rule.captures);
        this.stack.push({ rule, scopes: inner });
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
