import type { CoreState, Permissions, Tunnel } from "../api";

export function tunnel(over: Omit<Partial<Tunnel>, "permissions"> & { id: string; permissions?: Partial<Permissions> }): Tunnel {
  return {
    name: over.id,
    type: "https",
    publicUrl: `https://${over.id}.t.example.net`,
    localIp: "127.0.0.1",
    localPort: 3000,
    enabled: true,
    pausedByClient: false,
    expiresAt: null,
    display: {},
    ...over,
    permissions: { editLocal: true, loopbackOnly: false, toggle: true, ...over.permissions }
  };
}

export const notEnrolled: CoreState = {
  version: "v0.1.0",
  enrolled: false,
  configDir: "C:\\Users\\me\\AppData\\Roaming\\NyaTunnel",
  connected: false,
  tunnels: [],
  tunnelErrors: {},
  confirmed: {},
  canRequest: false,
  configRev: 0
};

/** An enrolled device; its tunnels count as confirmed unless `confirmed` is given. */
export function enrolled(over: Partial<CoreState> = {}): CoreState {
  const confirmed = Object.fromEntries((over.tunnels ?? []).map((t) => [t.id, true]));
  return {
    ...notEnrolled,
    confirmed,
    enrolled: true,
    server: "https://old.example.net",
    deviceId: "dev_1",
    deviceName: "dev-laptop",
    fingerprint: "SHA256:abcd",
    connected: true,
    configRev: 3,
    ...over
  };
}
