// Client settings that shape what the server is asked to do: which documents
// it sees and the initializationOptions it starts with.
//
// Like server-command.ts this module has no `vscode` import, so the rules
// (which languages are in the selector, how a setting value is normalized
// before it reaches the server) are unit tested under plain vitest. The
// extension reads the raw values from the configuration and passes them in.

import { CONFIG_SECTION, LANGUAGE_ID, expandHome } from "./server-command";

/**
 * C-family languages the server is offered for inline asm. It provides only
 * hover and diagnostics there; everything else is left to clangd (see
 * INLINE_ASM_FEATURES in extension.ts).
 */
export const INLINE_ASM_LANGUAGES: readonly string[] = ["c", "cpp"];

/** URI schemes the server can handle: on-disk files and unsaved buffers. */
export const SCHEMES: readonly string[] = ["file", "untitled"];

/** Target levels the server accepts; "" means "use the file comment or -march". */
export const LEVELS: readonly string[] = ["", "v1", "v2", "v3", "v4"];

/**
 * Every setting whose change needs a server restart. The server reads its
 * options once, from initializationOptions, so a restart is how a change
 * reaches it. `trace.server` is absent on purpose: languageclient applies
 * it live.
 */
export const RESTART_SETTINGS: readonly string[] = [
  "serverPath",
  "inlineAsm.enabled",
  "toolchain.enabled",
  "inlayHints.listing",
  "level",
  "compileCommands",
].map((s) => `${CONFIG_SECTION}.${s}`);

export interface DocumentFilter {
  language: string;
  scheme: string;
}

/** Document selector for the client: falcon-asm always, C and C++ when inline asm is on. */
export function documentSelector(inlineAsm: boolean): DocumentFilter[] {
  const languages = inlineAsm ? [LANGUAGE_ID, ...INLINE_ASM_LANGUAGES] : [LANGUAGE_ID];
  return languages.flatMap((language) => SCHEMES.map((scheme) => ({ language, scheme })));
}

/** True for documents the server fully owns (as opposed to C files it only annotates). */
export function isAsmDocument(languageId: string): boolean {
  return languageId === LANGUAGE_ID;
}

/** The initializationOptions object sent to the server. The Go side decodes exactly these keys. */
export interface InitializationOptions {
  /** Run the compiler/assembler from the compile database on open and save. */
  toolchain: boolean;
  /** Show listing inlay hints (offset and encoded bytes) after instruction lines. */
  listingHints: boolean;
  /** Target level override: "", "v1".."v4". "" defers to the file comment, then -march. */
  level: string;
  /** compile_commands.json to use instead of the discovered one; "" means discover. Absolute when non-empty and resolvable. */
  compileCommands: string;
}

/** Raw configuration values as read from `vscode.workspace.getConfiguration`, untyped on purpose. */
export interface RawSettings {
  toolchainEnabled?: unknown;
  inlayHintsListing?: unknown;
  level?: unknown;
  compileCommands?: unknown;
}

function bool(raw: unknown, fallback: boolean): boolean {
  return typeof raw === "boolean" ? raw : fallback;
}

/**
 * Normalizes a level: case and whitespace are forgiven ("V3 " -> "v3"), and
 * anything outside the enum becomes "" rather than reaching the server as a
 * value it would have to reject.
 */
export function normalizeLevel(raw: unknown): string {
  if (typeof raw !== "string") {
    return "";
  }
  const v = raw.trim().toLowerCase();
  return LEVELS.includes(v) ? v : "";
}

/**
 * Resolves the compileCommands override the same way serverPath is resolved
 * (`~/` expanded, relative paths against the first workspace folder), so
 * the server always receives a path it can open without knowing the
 * client's notion of "relative". Without a workspace a relative path is
 * passed through and the server resolves it against its root.
 */
export function normalizeCompileCommands(raw: unknown, homeDir: string, baseDir?: string): string {
  if (typeof raw !== "string") {
    return "";
  }
  const trimmed = raw.trim();
  if (trimmed === "") {
    return "";
  }
  const expanded = expandHome(trimmed, homeDir);
  const absolute = expanded.startsWith("/") || /^[A-Za-z]:[\\/]/.test(expanded) || expanded.startsWith("\\\\");
  if (absolute || baseDir === undefined) {
    return expanded;
  }
  const sep = baseDir.includes("\\") && !baseDir.includes("/") ? "\\" : "/";
  return baseDir.replace(/[\\/]+$/, "") + sep + expanded.replace(/^\.[\\/]/, "");
}

/** Builds initializationOptions from raw settings; every field has a defined value. */
export function initializationOptions(raw: RawSettings, homeDir: string, baseDir?: string): InitializationOptions {
  return {
    toolchain: bool(raw.toolchainEnabled, true),
    listingHints: bool(raw.inlayHintsListing, true),
    level: normalizeLevel(raw.level),
    compileCommands: normalizeCompileCommands(raw.compileCommands, homeDir, baseDir),
  };
}
