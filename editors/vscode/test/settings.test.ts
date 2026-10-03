import { describe, expect, it } from "vitest";
import { CONFIG_SECTION } from "../src/server-command";
import {
  LEVELS,
  RESTART_SETTINGS,
  documentSelector,
  initializationOptions,
  isAsmDocument,
  normalizeCompileCommands,
  normalizeLevel,
} from "../src/settings";

describe("documentSelector", () => {
  it("covers falcon-asm, C and C++ on file and untitled when inline asm is on", () => {
    expect(documentSelector(true)).toEqual([
      { language: "falcon-asm", scheme: "file" },
      { language: "falcon-asm", scheme: "untitled" },
      { language: "c", scheme: "file" },
      { language: "c", scheme: "untitled" },
      { language: "cpp", scheme: "file" },
      { language: "cpp", scheme: "untitled" },
    ]);
  });

  it("drops C and C++ when inline asm is off", () => {
    expect(documentSelector(false)).toEqual([
      { language: "falcon-asm", scheme: "file" },
      { language: "falcon-asm", scheme: "untitled" },
    ]);
  });
});

describe("isAsmDocument", () => {
  it.each([
    ["falcon-asm", true],
    ["c", false],
    ["cpp", false],
    ["asm", false],
  ])("%s -> %s", (id, want) => {
    expect(isAsmDocument(id)).toBe(want);
  });
});

describe("normalizeLevel", () => {
  it.each([
    [undefined, ""],
    [3, ""],
    ["", ""],
    ["v3", "v3"],
    [" V4 ", "v4"],
    ["v5", ""],
    ["x86-64-v3", ""],
  ])("%j -> %j", (raw, want) => {
    expect(normalizeLevel(raw)).toBe(want);
  });

  it("matches the enum declared in package.json order", () => {
    expect(LEVELS).toEqual(["", "v1", "v2", "v3", "v4"]);
  });
});

describe("normalizeCompileCommands", () => {
  it.each([
    [undefined, undefined, ""],
    [7, "/ws", ""],
    ["  ", "/ws", ""],
    ["/abs/compile_commands.json", "/ws", "/abs/compile_commands.json"],
    ["~/b/compile_commands.json", "/ws", "/home/u/b/compile_commands.json"],
    ["build/compile_commands.json", "/ws", "/ws/build/compile_commands.json"],
    ["./build/compile_commands.json", "/ws/", "/ws/build/compile_commands.json"],
    ["build/compile_commands.json", undefined, "build/compile_commands.json"],
    ["build\\cc.json", "C:\\ws", "C:\\ws\\build\\cc.json"],
    ["D:\\b\\cc.json", "C:\\ws", "D:\\b\\cc.json"],
  ])("%j with base %j -> %j", (raw, base, want) => {
    expect(normalizeCompileCommands(raw, "/home/u", base)).toBe(want);
  });
});

describe("initializationOptions", () => {
  it("uses the documented defaults when nothing is set", () => {
    expect(initializationOptions({}, "/home/u")).toEqual({
      toolchain: true,
      listingHints: true,
      level: "",
      compileCommands: "",
    });
  });

  it("passes set values through, normalized", () => {
    expect(
      initializationOptions(
        { toolchainEnabled: false, inlayHintsListing: false, level: "V2", compileCommands: "out/cc.json" },
        "/home/u",
        "/ws",
      ),
    ).toEqual({ toolchain: false, listingHints: false, level: "v2", compileCommands: "/ws/out/cc.json" });
  });

  it("ignores non-boolean switches instead of sending them", () => {
    const o = initializationOptions({ toolchainEnabled: "no", inlayHintsListing: 0 }, "/home/u");
    expect(o.toolchain).toBe(true);
    expect(o.listingHints).toBe(true);
  });

  it("has exactly the keys the server decodes", () => {
    expect(Object.keys(initializationOptions({}, "/h")).sort()).toEqual(
      ["compileCommands", "level", "listingHints", "toolchain"],
    );
  });
});

describe("RESTART_SETTINGS", () => {
  it("restarts on every setting the server reads at start, but not on trace", () => {
    expect(RESTART_SETTINGS).toEqual([
      `${CONFIG_SECTION}.serverPath`,
      `${CONFIG_SECTION}.inlineAsm.enabled`,
      `${CONFIG_SECTION}.toolchain.enabled`,
      `${CONFIG_SECTION}.inlayHints.listing`,
      `${CONFIG_SECTION}.level`,
      `${CONFIG_SECTION}.compileCommands`,
    ]);
    expect(RESTART_SETTINGS).not.toContain(`${CONFIG_SECTION}.trace.server`);
  });
});
