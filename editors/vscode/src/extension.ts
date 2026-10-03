// VS Code entry point for falcon-asm: a thin LSP client.
//
// Everything language-aware lives in the Go server. This file only finds the
// binary, starts it over stdio, and offers a restart. Path resolution is in
// server-command.ts so it can be unit tested without an extension host.

import * as fs from "fs";
import * as os from "os";
import * as vscode from "vscode";
import {
  LanguageClient,
  type LanguageClientOptions,
  type ServerOptions,
} from "vscode-languageclient/node";
import {
  CONFIG_SECTION,
  LANGUAGE_ID,
  resolveServerCommand,
  type HostProbe,
} from "./server-command";

const OPEN_SETTINGS = "Open Settings";

let client: LanguageClient | undefined;
let outputChannel: vscode.LogOutputChannel | undefined;

function isExecutable(path: string): boolean {
  try {
    if (!fs.statSync(path).isFile()) {
      return false;
    }
    // X_OK is meaningless on Windows (everything passes), which is fine:
    // there the PATHEXT match in server-command.ts is the real filter.
    fs.accessSync(path, fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

function hostProbe(): HostProbe {
  return {
    platform: process.platform,
    env: process.env,
    homeDir: os.homedir(),
    baseDir: vscode.workspace.workspaceFolders?.[0]?.uri.fsPath,
    isExecutable,
  };
}

async function reportMissingServer(message: string): Promise<void> {
  const choice = await vscode.window.showErrorMessage(message, OPEN_SETTINGS);
  if (choice === OPEN_SETTINGS) {
    await vscode.commands.executeCommand(
      "workbench.action.openSettings",
      `${CONFIG_SECTION}.serverPath`,
    );
  }
}

async function startClient(): Promise<void> {
  const config = vscode.workspace.getConfiguration(CONFIG_SECTION);
  const resolved = resolveServerCommand(config.get<unknown>("serverPath"), hostProbe());
  if (!resolved.ok) {
    // Not awaited: the notification stays up until dismissed and activation
    // should not block on the user reading it.
    void reportMissingServer(resolved.message);
    return;
  }

  // No transport is set on purpose: languageclient then spawns with plain
  // stdio pipes and does not append its own flags; the server's `--stdio`
  // is already in resolved.args.
  const serverOptions: ServerOptions = {
    command: resolved.command,
    args: resolved.args,
  };

  // One output channel for the whole session, so a restart does not leave a
  // trail of duplicate "falcon-asm" entries in the Output view.
  outputChannel ??= vscode.window.createOutputChannel("falcon-asm", { log: true });

  const clientOptions: LanguageClientOptions = {
    documentSelector: [
      { language: LANGUAGE_ID, scheme: "file" },
      { language: LANGUAGE_ID, scheme: "untitled" },
    ],
    outputChannel,
  };

  // The client id doubles as the settings prefix: languageclient reads
  // `falcon-asm.trace.server` on its own because of it.
  const next = new LanguageClient(CONFIG_SECTION, "falcon-asm", serverOptions, clientOptions);
  try {
    await next.start();
    client = next;
  } catch (err) {
    // The binary was found but would not run (wrong architecture, crash on
    // startup). Point at the same setting; the output channel has detail.
    const detail = err instanceof Error ? err.message : String(err);
    void reportMissingServer(
      `falcon-asm: failed to start "${resolved.command}": ${detail}. ` +
        `Check "${CONFIG_SECTION}.serverPath".`,
    );
    await next.dispose().catch(() => undefined);
  }
}

async function stopClient(): Promise<void> {
  const old = client;
  client = undefined;
  if (old !== undefined) {
    // dispose() stops the server (shutdown + exit) and frees the client.
    await old.dispose().catch(() => undefined);
  }
}

async function restartClient(): Promise<void> {
  // A full stop/start rather than client.restart(): the server path may
  // have changed, and the server options are fixed at construction time.
  await stopClient();
  await startClient();
}

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  context.subscriptions.push(
    vscode.commands.registerCommand("falcon-asm.restartServer", restartClient),
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration(`${CONFIG_SECTION}.serverPath`)) {
        void restartClient();
      }
    }),
    { dispose: () => void stopClient() },
  );
  await startClient();
}

export async function deactivate(): Promise<void> {
  await stopClient();
  outputChannel?.dispose();
  outputChannel = undefined;
}
