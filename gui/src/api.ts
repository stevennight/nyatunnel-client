// Thin wrappers around the Tauri commands. Every daemon call goes through the Rust `core` command,
// which adds the IPC token; the token never reaches the web view.
import { invoke } from "@tauri-apps/api/core";

export interface Permissions {
  editLocal: boolean;
  loopbackOnly: boolean;
  toggle: boolean;
}

export interface Display {
  accessPolicy?: string;
  limits?: string;
}

export type TunnelType = "https" | "tcp" | "udp" | "tcpudp";

export interface Tunnel {
  id: string;
  name: string;
  type: TunnelType | string;
  publicUrl: string;
  localIp: string;
  localPort: number;
  enabled: boolean;
  pausedByClient: boolean;
  expiresAt?: string | null;
  permissions: Permissions;
  display: Display;
}

export interface ServiceState {
  running: boolean;
  server: string;
  deviceId: string;
  deviceName: string;
  detail?: string;
  /** The service runs this app's own nyatunnel.exe (older installs): the app must not update itself. */
  sharesCore?: boolean;
}

export interface CoreState {
  version: string;
  enrolled: boolean;
  server?: string;
  deviceId?: string;
  deviceName?: string;
  fingerprint?: string;
  configDir: string;
  connected: boolean;
  connectedAt?: string | null;
  /** How the session reaches the server: "direct" or "proxy" (through the 443 reverse proxy). */
  transport?: string;
  lastError?: string;
  notice?: string;
  tunnels: Tunnel[];
  tunnelErrors: Record<string, string>;
  canRequest: boolean;
  configRev: number;
  service?: ServiceState | null;
}

export interface LogLine {
  seq: number;
  /** Set by useLogs: which core run the line came from (seq restarts with the core). */
  run?: number;
  time: string;
  level: string;
  text: string;
}

export interface ParsedLink {
  kind: "enroll" | "open";
  server: string;
  code?: string;
  tunnelId?: string;
  switchServer: boolean;
}

export interface EnrollPreview {
  serverName: string;
  owner: string;
  deviceNameHint: string;
  tunnels: { name: string; publicUrl: string }[] | null;
  expiresAt: number;
}

export interface TunnelRequestBody {
  type: string;
  subdomain: string;
  localIp: string;
  localPort: number;
  duration: "24h" | "7d" | "30d" | "";
  reason: string;
}

export interface UpdateInfo {
  current: string;
  latest: string;
  newer: boolean;
  url: string;
  /** Outcome of the last unattended install (written by the updater). */
  lastAttempt?: UpdateAttempt | null;
}

export interface UpdateAttempt {
  from: string;
  to: string;
  ok: boolean;
  rolledBack?: boolean;
  error?: string;
  at: string;
}

/** A failed version is not retried automatically for a day (each try interrupts the tunnels). */
export function recentlyFailed(u: UpdateInfo): boolean {
  const a = u.lastAttempt;
  return !!a && !a.ok && a.to.replace(/^v/, "") === u.latest.replace(/^v/, "") && Date.now() - Date.parse(a.at) < 24 * 3600 * 1000;
}

/** Status of the bundled core process, maintained by the Rust side. */
export interface CoreStatus {
  state: "starting" | "running" | "error" | "stopped";
  version?: string | null;
  error?: string | null;
  restarts: number;
}

/** Error shape returned by the `core` command (daemon errors are passed through). */
export class CoreError extends Error {
  constructor(public code: string, message: string, public status = 0) {
    super(message || code);
    this.name = "CoreError";
  }
}

function toCoreError(e: unknown): CoreError {
  if (e instanceof CoreError) return e;
  if (e && typeof e === "object") {
    const o = e as { error?: string; message?: string; status?: number };
    return new CoreError(o.error ?? "error", o.message ?? "", o.status ?? 0);
  }
  return new CoreError("error", String(e));
}

export async function core<T>(method: "GET" | "POST", path: string, body?: unknown): Promise<T> {
  try {
    return await invoke<T>("core", { method, path, body: body ?? null });
  } catch (e) {
    throw toCoreError(e);
  }
}

export const api = {
  state: () => core<CoreState>("GET", "/v1/state"),
  logs: (after: number) => core<{ lines: LogLine[] }>("GET", `/v1/logs?after=${after}`),
  parseLink: (url: string) => core<ParsedLink>("POST", "/v1/link", { url }),
  preview: (server: string, code: string) =>
    core<{ preview: EnrollPreview; server: string }>("POST", "/v1/enroll/preview", { server, code }),
  enroll: (server: string, code: string, name: string) =>
    core<CoreState>("POST", "/v1/enroll", { server, code, name }),
  logout: () => core<CoreState>("POST", "/v1/logout"),
  updateTunnel: (id: string, change: { localIp?: string; localPort?: number; paused?: boolean }) =>
    core<{ ok: boolean }>("POST", `/v1/tunnels/${encodeURIComponent(id)}`, change),
  request: (body: TunnelRequestBody) => core<{ ok: boolean }>("POST", "/v1/requests", body),
  checkUpdate: (force = false) => core<UpdateInfo>("GET", force ? "/v1/update?force=1" : "/v1/update"),
  /**
   * Downloads the latest release's installer (verified by the core) and installs it. On Windows
   * and with an AppImage the app is replaced and restarted, so this only returns on failure or
   * when the package was opened for the user (macOS dmg, Linux deb). Resolves to the kind.
   */
  installUpdate: async () => {
    try {
      return await invoke<string>("install_update");
    } catch (e) {
      throw toCoreError(e);
    }
  },
  coreStatus: () => invoke<CoreStatus>("core_status"),
  takePendingLinks: () => invoke<string[]>("take_pending_links"),
  autostartEnabled: () => invoke<boolean>("autostart_status"),
  setAutostart: (enabled: boolean) => invoke<boolean>("autostart_set", { enabled }),
  quit: () => invoke<void>("quit_app")
};

export const RELEASES_URL = "https://github.com/stevennight/nyatunnel-client/releases";

const AUTO_UPDATE_KEY = "nyatunnel.autoUpdate";

/** Whether new versions are downloaded and installed without asking (default on, per user). */
export function autoUpdateEnabled(): boolean {
  try {
    return localStorage.getItem(AUTO_UPDATE_KEY) !== "off";
  } catch {
    return true;
  }
}

export function setAutoUpdateEnabled(on: boolean) {
  try {
    localStorage.setItem(AUTO_UPDATE_KEY, on ? "on" : "off");
  } catch {
    // Storage unavailable: the default (on) applies.
  }
}
