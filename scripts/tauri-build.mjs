#!/usr/bin/env node
// Runs `tauri build` for gui/. With NYATUNNEL_VERSION set (release builds), the version is
// injected through a temporary config file so package.json does not have to be edited.
// Extra arguments are passed through, e.g.:
//   node scripts/tauri-build.mjs --bundles nsis
//   node scripts/tauri-build.mjs --target aarch64-apple-darwin --bundles dmg
import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const guiRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..", "gui");
const tauriCli = resolve(guiRoot, "node_modules", "@tauri-apps", "cli", "tauri.js");
const releaseConfigPath = resolve(guiRoot, "src-tauri", ".tauri-release.conf.json");
const releaseVersion = (process.env.NYATUNNEL_VERSION?.trim() ?? "").replace(/^v/, "");
const semanticVersion = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

if (releaseVersion && !semanticVersion.test(releaseVersion)) {
  throw new Error(`NYATUNNEL_VERSION must be a semantic version; received ${JSON.stringify(releaseVersion)}.`);
}
if (!existsSync(tauriCli)) {
  throw new Error("Tauri CLI not found; run `npm ci` in gui/ first.");
}

const previous = releaseVersion && existsSync(releaseConfigPath) ? readFileSync(releaseConfigPath, "utf8") : null;

try {
  const command = [tauriCli, "build", "--ci"];
  if (releaseVersion) {
    writeFileSync(releaseConfigPath, `${JSON.stringify({ version: releaseVersion }, null, 2)}\n`, "utf8");
    command.push("--config", releaseConfigPath);
  }
  command.push(...process.argv.slice(2));

  const result = spawnSync(process.execPath, command, { cwd: guiRoot, env: process.env, stdio: "inherit" });
  if (result.error) throw result.error;
  process.exitCode = result.status ?? 1;
} finally {
  if (releaseVersion) {
    if (previous === null) rmSync(releaseConfigPath, { force: true });
    else writeFileSync(releaseConfigPath, previous, "utf8");
  }
}
