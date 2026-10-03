// Minimal ambient declarations for the handful of Node built-ins this
// extension and its tests use.
//
// @types/node is not an approved dependency for this client (the dependency
// list is kept to the languageclient plus build and test tooling), so the
// few signatures we actually call are declared here instead. Keep this file
// to exactly what src/ and test/ use; anything more belongs in a real
// @types/node dependency, which would replace this file outright.

declare module "fs" {
  export const constants: { readonly X_OK: number };
  export function accessSync(path: string, mode?: number): void;
  export function existsSync(path: string): boolean;
  export function statSync(path: string): { isFile(): boolean; size: number };
  export function readFileSync(path: string, encoding: "utf8"): string;
  export function readdirSync(path: string): string[];
  export function writeFileSync(path: string, data: string): void;
  export function mkdtempSync(prefix: string): string;
  export function mkdirSync(path: string, options?: { recursive?: boolean }): void;
  export function chmodSync(path: string, mode: number): void;
  export function rmSync(path: string, options?: { recursive?: boolean; force?: boolean }): void;
}

declare module "os" {
  export function homedir(): string;
  export function tmpdir(): string;
}

declare module "path" {
  export const sep: string;
  export function join(...parts: string[]): string;
  export function resolve(...parts: string[]): string;
  export function dirname(p: string): string;
}

declare module "url" {
  export function fileURLToPath(url: string): string;
}

// vscode-languageclient's node entry point imports child_process for its
// own types; we never touch them, but the module has to exist for tsc.
declare module "child_process" {
  export interface ChildProcess {}
  export interface SpawnOptions {}
  export interface ForkOptions {}
}

declare const process: {
  readonly platform: string;
  readonly env: Record<string, string | undefined>;
};

interface ImportMeta {
  readonly url: string;
}
