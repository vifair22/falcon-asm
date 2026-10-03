// Bundles the client and vscode-languageclient into one CommonJS file so the
// .vsix ships without node_modules (hence `vsce package --no-dependencies`).
import * as esbuild from "esbuild";

const watch = process.argv.includes("--watch");
const production = process.argv.includes("--production");

const options = {
  entryPoints: ["src/extension.ts"],
  bundle: true,
  outfile: "out/extension.js",
  format: "cjs",
  platform: "node",
  target: "node22",
  // Provided by the extension host at runtime; never bundle it.
  external: ["vscode"],
  sourcemap: !production,
  minify: production,
  logLevel: "info",
};

if (watch) {
  const ctx = await esbuild.context(options);
  await ctx.watch();
} else {
  await esbuild.build(options);
}
