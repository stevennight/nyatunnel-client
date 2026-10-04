import type { Tunnel } from "./api";

/** Host (with port, if any) of a server URL such as https://tunnel.example.net. */
export function hostOf(server: string | undefined | null): string {
  if (!server) return "";
  try {
    return new URL(server).host;
  } catch {
    return server.replace(/^https?:\/\//, "").replace(/\/.*$/, "");
  }
}

export function isLoopback(ip: string): boolean {
  const v = ip.trim().toLowerCase();
  return v === "localhost" || v === "::1" || v === "[::1]" || /^127(\.\d{1,3}){3}$/.test(v);
}

export function isValidPort(port: number): boolean {
  return Number.isInteger(port) && port >= 1 && port <= 65535;
}

export function typeLabel(type: string): string {
  return type === "tcpudp" ? "TCP+UDP" : type.toUpperCase();
}

/** Whether the public address is something a browser can open. */
export function isOpenable(url: string): boolean {
  return /^https?:\/\//i.test(url);
}

export type TunnelStatusKind = "ok" | "paused" | "disabled" | "expired" | "error";

export interface TunnelStatus {
  kind: TunnelStatusKind;
  label: string;
}

export function tunnelStatus(t: Tunnel, error: string | undefined, now = Date.now()): TunnelStatus {
  if (!t.enabled) return { kind: "disabled", label: "已被管理员停用" };
  if (t.expiresAt && Date.parse(t.expiresAt) <= now) return { kind: "expired", label: "已过期" };
  if (t.pausedByClient) return { kind: "paused", label: "已由你暂停" };
  if (error) return { kind: "error", label: error };
  return { kind: "ok", label: "运行中" };
}

/** "3 小时后到期" style label, or "" when the tunnel never expires. */
export function expiryLabel(expiresAt: string | null | undefined, now = Date.now()): string {
  if (!expiresAt) return "";
  const ms = Date.parse(expiresAt) - now;
  if (Number.isNaN(ms)) return "";
  if (ms <= 0) return "已过期";
  const h = ms / 3_600_000;
  if (h < 1) return `${Math.max(1, Math.round(ms / 60_000))} 分钟后到期`;
  if (h < 48) return `${Math.round(h)} 小时后到期`;
  return `${Math.round(h / 24)} 天后到期`;
}

export function formatTime(iso: string | null | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

export function clockTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

export function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message;
  return String(e);
}

export function transportLabel(transport: string | undefined): string {
  if (transport === "direct") return "直连";
  if (transport === "proxy") return "经 443";
  return "";
}
