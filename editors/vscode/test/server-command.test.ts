import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  DEFAULT_SERVER_PATH,
  SERVER_ARGS,
  expandHome,
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
