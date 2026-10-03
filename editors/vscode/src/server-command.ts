// Resolution of the falcon-asm server command from user settings.
//
// This module deliberately has no `vscode` import and no direct filesystem
// access: everything host-specific comes in through the HostProbe argument.
// That keeps the only logic in the client that can actually go wrong (where
// is the binary, what do we tell the user when it is missing) testable under
// plain vitest without an extension host.

/** Name of the binary looked up on PATH when the setting is empty. */
export const DEFAULT_SERVER_PATH = "falcon-asm";

/** Settings section; also the languageclient id, so `falcon-asm.trace.server` is picked up by the client itself. */
export const CONFIG_SECTION = "falcon-asm";

/** Language id contributed in package.json; the server does not care, but the document selector must match. */
export const LANGUAGE_ID = "falcon-asm";

/**
 * Arguments passed to the server. The server only speaks stdio and accepts
 * `--stdio` as a no-op; passing it explicitly mirrors what other clients
 * (LSP4IJ, Neovim) send, so every editor exercises the same entrypoint.
 */
export const SERVER_ARGS: readonly string[] = ["--stdio"];

/** Everything about the host that resolution needs, injected for testability. */
export interface HostProbe {
  /** `process.platform`; only "win32" changes behaviour (separator, PATHEXT). */
  platform: string;
  /** Environment used for PATH and PATHEXT lookup. */
  env: Readonly<Record<string, string | undefined>>;
  /** Home directory for `~/` expansion. */
  homeDir: string;
  /** Directory relative paths are resolved against (first workspace folder), if any. */
  baseDir?: string;
  /** True when `path` exists, is a regular file and is executable by us. */
  isExecutable(path: string): boolean;
}

export type ResolveResult =
  | { ok: true; command: string; args: string[] }
  | { ok: false; requested: string; message: string };

/** Turns whatever the settings store holds into a usable path string. */
export function normalizeServerPath(raw: unknown): string {
  if (typeof raw !== "string") {
    return DEFAULT_SERVER_PATH;
  }
  const trimmed = raw.trim();
  return trimmed === "" ? DEFAULT_SERVER_PATH : trimmed;
}

function isWindows(probe: HostProbe): boolean {
  return probe.platform === "win32";
}

function pathSep(probe: HostProbe): string {
  return isWindows(probe) ? "\\" : "/";
}

function joinPath(probe: HostProbe, dir: string, name: string): string {
  const sep = pathSep(probe);
  if (dir.endsWith("/") || dir.endsWith(sep)) {
    return dir + name;
  }
  return dir + sep + name;
}

function hasDirComponent(p: string): boolean {
  return p.includes("/") || p.includes("\\");
}

function isAbsolute(probe: HostProbe, p: string): boolean {
  if (p.startsWith("/")) {
    return true;
  }
  // Drive-letter and UNC paths only mean something on Windows.
  return isWindows(probe) && (/^[A-Za-z]:[\\/]/.test(p) || p.startsWith("\\\\"));
}

/** Expands a leading `~` the way a shell would, since settings are not shell-expanded. */
export function expandHome(p: string, homeDir: string): string {
  if (p === "~") {
    return homeDir;
  }
  if (p.startsWith("~/") || p.startsWith("~\\")) {
    return homeDir.replace(/[\\/]+$/, "") + p.slice(1);
  }
  return p;
}

/**
 * Candidate file names for one base path. On Windows a bare `falcon-asm`
 * has to be tried with each PATHEXT extension, the same way cmd.exe does.
 */
function candidates(probe: HostProbe, base: string): string[] {
  if (!isWindows(probe)) {
    return [base];
  }
  const exts = (probe.env.PATHEXT ?? ".EXE;.CMD;.BAT;.COM")
    .split(";")
    .map((e) => e.trim())
    .filter((e) => e !== "");
  const lower = base.toLowerCase();
  if (exts.some((e) => lower.endsWith(e.toLowerCase()))) {
    return [base];
  }
  return [base, ...exts.map((e) => base + e)];
}

function firstExecutable(probe: HostProbe, base: string): string | undefined {
  return candidates(probe, base).find((c) => probe.isExecutable(c));
}

/** Searches PATH for a bare command name; undefined when not found. */
export function searchPath(name: string, probe: HostProbe): string | undefined {
  // Windows environments are case-insensitive and often spell it "Path".
  const raw = probe.env.PATH ?? probe.env.Path ?? "";
  const delim = isWindows(probe) ? ";" : ":";
  for (const dir of raw.split(delim)) {
    if (dir === "") {
      // An empty PATH entry means the current directory to a shell, which is
      // not a meaningful place for an editor-launched server; skip it.
      continue;
    }
    const hit = firstExecutable(probe, joinPath(probe, dir, name));
    if (hit !== undefined) {
      return hit;
    }
  }
  return undefined;
}

/**
 * Resolves the configured server path to an absolute executable.
 *
 * Resolving up front, instead of letting child_process report ENOENT, is
 * what lets the extension show one actionable error naming the setting,
 * rather than a stack of "Connection to server got closed" notifications
 * from the client's restart loop.
 */
export function resolveServerCommand(raw: unknown, probe: HostProbe): ResolveResult {
  const requested = normalizeServerPath(raw);
  const expanded = expandHome(requested, probe.homeDir);

  let found: string | undefined;
  if (hasDirComponent(expanded)) {
    let full = expanded;
    if (!isAbsolute(probe, expanded) && probe.baseDir !== undefined) {
      full = joinPath(probe, probe.baseDir, expanded.replace(/^\.[\\/]/, ""));
    }
    found = firstExecutable(probe, full);
  } else {
    found = searchPath(expanded, probe);
  }

  if (found === undefined) {
    return { ok: false, requested, message: notFoundMessage(requested) };
  }
  return { ok: true, command: found, args: [...SERVER_ARGS] };
}

/** User-facing text when the binary cannot be found; names the setting so the fix is obvious. */
export function notFoundMessage(requested: string): string {
  const where = hasDirComponent(requested) || requested.startsWith("~")
    ? `"${requested}"`
    : `"${requested}" on PATH`;
  return (
    `falcon-asm: language server not found (looked for ${where}). ` +
    `Install it with \`make install\` in the falcon-asm repo, or set ` +
    `"${CONFIG_SECTION}.serverPath" to the binary's full path.`
  );
}
