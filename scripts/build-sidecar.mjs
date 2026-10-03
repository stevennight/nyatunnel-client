#!/usr/bin/env node
// Builds the Go core (cmd/nyatunnel) as the Tauri sidecar:
//   gui/src-tauri/binaries/nyatunnel-<rust target triple>[.exe]
//
// Usage: node scripts/build-sidecar.mjs [--target <triple>]
// The triple defaults to $TAURI_TARGET_TRIPLE, then to the host triple reported by rustc.
// Build metadata matches release.yml: NYATUNNEL_VERSION (else VERSION), NYATUNNEL_COMMIT
// (else $GITHUB_SHA, else `git rev-parse HEAD`), NYATUNNEL_BUILD_DATE (else now, UTC).
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const outDir = resolve(root, "gui", "src-tauri", "binaries");

const targets = {
  "x86_64-pc-windows-msvc": ["windows", "amd64"],
  "aarch64-pc-windows-msvc": ["windows", "arm64"],
  "x86_64-apple-darwin": ["darwin", "amd64"],
  "aarch64-apple-darwin": ["darwin", "arm64"],
  "x86_64-unknown-linux-gnu": ["linux", "amd64"],
  "aarch64-unknown-linux-gnu": ["linux", "arm64"]
};

function arg(name) {
  const i = process.argv.indexOf(name);
  return i >= 0 ? process.argv[i + 1] : undefined;
}

function hostTriple() {
  const out = execFileSync("rustc", ["-vV"], { encoding: "utf8" });
  const m = /^host:\s*(\S+)/m.exec(out);
  if (!m) throw new Error("cannot determine the host target triple from `rustc -vV`");
  return m[1];
}

function gitCommit() {
  try {
    return execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim();
  } catch {
    return "";
  }
}

const triple = arg("--target") || process.env.TAURI_TARGET_TRIPLE?.trim() || hostTriple();
const goTarget = targets[triple];
if (!goTarget) {
  throw new Error(`unsupported target triple ${triple}; known: ${Object.keys(targets).join(", ")}`);
}
const [goos, goarch] = goTarget;

const version = (process.env.NYATUNNEL_VERSION?.trim() || readFileSync(resolve(root, "VERSION"), "utf8").trim()).replace(/^v/, "");
if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version)) {
  throw new Error(`invalid version ${JSON.stringify(version)}`);
}
const commit = process.env.NYATUNNEL_COMMIT?.trim() || process.env.GITHUB_SHA?.trim() || gitCommit();
const buildDate = process.env.NYATUNNEL_BUILD_DATE?.trim() || new Date().toISOString().replace(/\.\d{3}Z$/, "Z");

const pkg = "nyatunnel-client/internal/shared/version";
const ldflags = [
  "-s", "-w",
  `-X ${pkg}.Version=v${version}`,
  `-X ${pkg}.Commit=${commit}`,
  `-X ${pkg}.BuildDate=${buildDate}`
].join(" ");

const exe = goos === "windows" ? ".exe" : "";
const out = resolve(outDir, `nyatunnel-${triple}${exe}`);
mkdirSync(outDir, { recursive: true });

console.log(`building sidecar ${goos}/${goarch} v${version} -> ${out}`);
const result = spawnSync(
  "go",
  ["build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", out, "./cmd/nyatunnel"],
  { cwd: root, stdio: "inherit", env: { ...process.env, CGO_ENABLED: "0", GOOS: goos, GOARCH: goarch } }
);
if (result.error) throw result.error;
process.exit(result.status ?? 1);
