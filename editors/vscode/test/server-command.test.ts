import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  DEFAULT_SERVER_PATH,
  SERVER_ARGS,
  expandHome,
  fallbackDirs,
  normalizeServerPath,
  notFoundMessage,
  resolveServerCommand,
  searchPath,
  type HostProbe,
} from "../src/server-command";

// A fake host: the set of paths that "exist and are executable". Most cases
// use this so they run the same on every OS; one block below goes to the
// real filesystem to prove the probe contract matches what Node reports.
function fakeProbe(executables: string[], over: Partial<HostProbe> = {}): HostProbe {
  const set = new Set(executables);
  return {
    platform: "linux",
    env: { PATH: "/usr/local/bin:/usr/bin" },
    homeDir: "/home/u",
    isExecutable: (p) => set.has(p),
    ...over,
  };
}

describe("normalizeServerPath", () => {
  it.each([
    [undefined, DEFAULT_SERVER_PATH],
    [null, DEFAULT_SERVER_PATH],
    [42, DEFAULT_SERVER_PATH],
    ["", DEFAULT_SERVER_PATH],
    ["   ", DEFAULT_SERVER_PATH],
    ["  /opt/fa/bin/falcon-asm ", "/opt/fa/bin/falcon-asm"],
    ["fa", "fa"],
  ])("%j -> %j", (raw, want) => {
    expect(normalizeServerPath(raw)).toBe(want);
  });
});

describe("expandHome", () => {
  it.each([
    ["~", "/home/u", "/home/u"],
    ["~/bin/fa", "/home/u", "/home/u/bin/fa"],
    ["~/bin/fa", "/home/u/", "/home/u/bin/fa"],
    ["~other/bin", "/home/u", "~other/bin"],
    ["/abs/fa", "/home/u", "/abs/fa"],
    ["fa~", "/home/u", "fa~"],
  ])("%s with home %s -> %s", (p, home, want) => {
    expect(expandHome(p, home)).toBe(want);
  });
});

describe("searchPath", () => {
  it("returns the first PATH hit in order", () => {
    const probe = fakeProbe(["/usr/bin/falcon-asm", "/usr/local/bin/falcon-asm"]);
    expect(searchPath("falcon-asm", probe)).toBe("/usr/local/bin/falcon-asm");
  });

  it("skips empty PATH entries instead of meaning the cwd", () => {
    const probe = fakeProbe(["falcon-asm", "/b/falcon-asm"], { env: { PATH: "::/b/" } });
    expect(searchPath("falcon-asm", probe)).toBe("/b/falcon-asm");
  });

  it("returns undefined when PATH is unset", () => {
    expect(searchPath("falcon-asm", fakeProbe(["/usr/bin/falcon-asm"], { env: {} }))).toBeUndefined();
  });

  it("uses ';' and PATHEXT on Windows, and accepts the 'Path' spelling", () => {
    const probe = fakeProbe(["C:\\tools\\falcon-asm.EXE"], {
      platform: "win32",
      env: { Path: "C:\\nope;C:\\tools", PATHEXT: ".COM;.EXE" },
    });
    expect(searchPath("falcon-asm", probe)).toBe("C:\\tools\\falcon-asm.EXE");
  });

  it("falls back to a default PATHEXT on Windows", () => {
    const probe = fakeProbe(["C:\\t\\falcon-asm.CMD"], { platform: "win32", env: { PATH: "C:\\t" } });
    expect(searchPath("falcon-asm", probe)).toBe("C:\\t\\falcon-asm.CMD");
  });

  it("does not add PATHEXT when the name already carries one", () => {
    const probe = fakeProbe(["C:\\t\\fa.exe"], { platform: "win32", env: { PATH: "C:\\t" } });
    expect(searchPath("fa.exe", probe)).toBe("C:\\t\\fa.exe");
  });
});

describe("resolveServerCommand", () => {
  it("resolves the default name on PATH and passes --stdio", () => {
    const r = resolveServerCommand(undefined, fakeProbe(["/usr/bin/falcon-asm"]));
    expect(r).toEqual({ ok: true, command: "/usr/bin/falcon-asm", args: ["--stdio"] });
  });

  it("returns a fresh args array each time", () => {
    const r = resolveServerCommand("", fakeProbe(["/usr/bin/falcon-asm"]));
    expect(r.ok && r.args).not.toBe(SERVER_ARGS);
  });

  it("uses an absolute path as is, without consulting PATH", () => {
    const r = resolveServerCommand("/opt/fa", fakeProbe(["/opt/fa", "/usr/bin/fa"]));
    expect(r.ok && r.command).toBe("/opt/fa");
  });

  it("expands ~/ before checking", () => {
    const r = resolveServerCommand("~/.local/bin/falcon-asm", fakeProbe(["/home/u/.local/bin/falcon-asm"]));
    expect(r.ok && r.command).toBe("/home/u/.local/bin/falcon-asm");
  });

  it("resolves a relative path against the workspace folder", () => {
    const probe = fakeProbe(["/ws/build/bin/falcon-asm"], { baseDir: "/ws" });
    expect(resolveServerCommand("./build/bin/falcon-asm", probe)).toMatchObject({
      ok: true,
      command: "/ws/build/bin/falcon-asm",
    });
    expect(resolveServerCommand("build/bin/falcon-asm", probe)).toMatchObject({ ok: true });
  });

  it("leaves a relative path alone when there is no workspace", () => {
    const r = resolveServerCommand("build/fa", fakeProbe(["build/fa"]));
    expect(r.ok && r.command).toBe("build/fa");
  });

  it("treats drive-letter paths as absolute on Windows", () => {
    const probe = fakeProbe(["D:\\fa\\falcon-asm.exe"], { platform: "win32", baseDir: "C:\\ws" });
    const r = resolveServerCommand("D:\\fa\\falcon-asm.exe", probe);
    expect(r.ok && r.command).toBe("D:\\fa\\falcon-asm.exe");
  });

  it("fails with a message naming PATH for a bare name", () => {
    const r = resolveServerCommand("falcon-asm", fakeProbe([]));
    expect(r.ok).toBe(false);
    if (!r.ok) {
      expect(r.requested).toBe("falcon-asm");
      expect(r.message).toContain('"falcon-asm" on PATH');
      expect(r.message).toContain("falcon-asm.serverPath");
    }
  });

  it("fails with a message naming the path for an explicit path", () => {
    const r = resolveServerCommand("~/nope/fa", fakeProbe([]));
    expect(!r.ok && r.message).toContain('(looked for "~/nope/fa")');
  });
});

describe("fallback directories", () => {
  it("lists ~/.local/bin, $GOBIN, each $GOPATH bin and ~/go/bin, in that order", () => {
    const probe = fakeProbe([], { env: { GOBIN: "/opt/gobin", GOPATH: "/gp1::/gp2/" } });
    expect(fallbackDirs(probe)).toEqual([
      "/home/u/.local/bin",
      "/opt/gobin",
      "/gp1/bin",
      "/gp2/bin",
      "/home/u/go/bin",
    ]);
  });

  it("drops the repeat when GOPATH is the default ~/go", () => {
    const probe = fakeProbe([], { env: { GOPATH: "/home/u/go" }, homeDir: "/home/u/" });
    expect(fallbackDirs(probe)).toEqual(["/home/u/.local/bin", "/home/u/go/bin"]);
  });

  it("uses ';' and backslashes on Windows", () => {
    const probe = fakeProbe([], { platform: "win32", env: { GOPATH: "C:\\gp;D:\\gp" }, homeDir: "C:\\Users\\u" });
    expect(fallbackDirs(probe)).toEqual([
      "C:\\Users\\u\\.local\\bin",
      "C:\\gp\\bin",
      "D:\\gp\\bin",
      "C:\\Users\\u\\go\\bin",
    ]);
  });

  it("finds the default name in ~/.local/bin when PATH lacks it", () => {
    // The KDE session case: PATH is the session's, without ~/.local/bin.
    const r = resolveServerCommand(undefined, fakeProbe(["/home/u/.local/bin/falcon-asm"]));
    expect(r).toEqual({ ok: true, command: "/home/u/.local/bin/falcon-asm", args: ["--stdio"] });
  });

  it("prefers PATH over the fallbacks", () => {
    const r = resolveServerCommand("", fakeProbe(["/home/u/.local/bin/falcon-asm", "/usr/bin/falcon-asm"]));
    expect(r.ok && r.command).toBe("/usr/bin/falcon-asm");
  });

  it.each([
    [{ GOBIN: "/opt/gobin" }, "/opt/gobin/falcon-asm"],
    [{ GOPATH: "/gp" }, "/gp/bin/falcon-asm"],
    [{}, "/home/u/go/bin/falcon-asm"],
  ])("finds a go install under %j", (env, where) => {
    const r = resolveServerCommand("falcon-asm", fakeProbe([where], { env: { PATH: "/usr/bin", ...env } }));
    expect(r.ok && r.command).toBe(where);
  });

  it("takes ~/.local/bin before a go install", () => {
    const probe = fakeProbe(["/home/u/go/bin/falcon-asm", "/home/u/.local/bin/falcon-asm"]);
    expect(resolveServerCommand(undefined, probe)).toMatchObject({ command: "/home/u/.local/bin/falcon-asm" });
  });

  it("applies PATHEXT in the fallback directories on Windows", () => {
    const probe = fakeProbe(["C:\\Users\\u\\go\\bin\\falcon-asm.EXE"], {
      platform: "win32",
      env: { PATH: "C:\\nope" },
      homeDir: "C:\\Users\\u",
    });
    expect(resolveServerCommand(undefined, probe)).toMatchObject({ command: "C:\\Users\\u\\go\\bin\\falcon-asm.EXE" });
  });

  it("never falls back for another bare name or an explicit path", () => {
    expect(resolveServerCommand("fa", fakeProbe(["/home/u/.local/bin/fa"])).ok).toBe(false);
    expect(resolveServerCommand("/opt/falcon-asm", fakeProbe(["/home/u/.local/bin/falcon-asm"])).ok).toBe(false);
  });

  it("names the fallback locations when the default is not found anywhere", () => {
    const r = resolveServerCommand(undefined, fakeProbe([]));
    expect(!r.ok && r.message).toContain("~/.local/bin");
    expect(!r.ok && r.message).toContain("~/go/bin");
    const other = resolveServerCommand("fa", fakeProbe([]));
    expect(!other.ok && other.message).not.toContain("~/.local/bin");
  });
});

describe("notFoundMessage", () => {
  it("is pure ASCII", () => {
    // The message ends up in a notification; keep it within the repo rule.
    expect(notFoundMessage("falcon-asm")).toMatch(/^[\x20-\x7e]*$/);
  });
});

// Proves the HostProbe contract the extension implements (a regular file
// with the execute bit) lines up with resolution on a real filesystem.
describe("resolveServerCommand on a real directory", () => {
  let dir = "";

  function realIsExecutable(p: string): boolean {
    try {
      if (!fs.statSync(p).isFile()) {
        return false;
      }
      fs.accessSync(p, fs.constants.X_OK);
      return true;
    } catch {
      return false;
    }
  }

  beforeAll(() => {
    dir = fs.mkdtempSync(path.join(os.tmpdir(), "falcon-asm-vsc-"));
    fs.writeFileSync(path.join(dir, "falcon-asm"), "#!/bin/sh\n");
    fs.chmodSync(path.join(dir, "falcon-asm"), 0o755);
    fs.writeFileSync(path.join(dir, "not-exec"), "");
    fs.chmodSync(path.join(dir, "not-exec"), 0o644);
    fs.mkdirSync(path.join(dir, "subdir"));
  });

  afterAll(() => {
    fs.rmSync(dir, { recursive: true, force: true });
  });

  const probe = (): HostProbe => ({
    platform: process.platform,
    env: { PATH: dir },
    homeDir: os.homedir(),
    isExecutable: realIsExecutable,
  });

  it.skipIf(process.platform === "win32")("finds an executable file on PATH", () => {
    expect(resolveServerCommand(undefined, probe())).toMatchObject({
      ok: true,
      command: path.join(dir, "falcon-asm"),
    });
  });

  it.skipIf(process.platform === "win32")("rejects a file without the execute bit", () => {
    expect(resolveServerCommand("not-exec", probe()).ok).toBe(false);
  });

  it("rejects a directory", () => {
    expect(resolveServerCommand(path.join(dir, "subdir"), probe()).ok).toBe(false);
  });
});
