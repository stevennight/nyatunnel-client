// In-memory stand-ins for the Tauri APIs used by the app (wired up in setup.ts).
type Handler = (args: Record<string, unknown>) => unknown;

export interface Call {
  cmd: string;
  args: Record<string, unknown>;
}

export const calls: Call[] = [];
const handlers = new Map<string, Handler>();
const listeners = new Map<string, Set<(e: { payload: unknown }) => void>>();

/**
 * Registers responses. Keys are Tauri command names, or "METHOD /path" for the `core` command
 * (query strings are ignored). A handler that throws makes the invoke reject with that value.
 */
export function mockCommands(map: Record<string, Handler | unknown>) {
  for (const [k, v] of Object.entries(map)) {
    handlers.set(k, typeof v === "function" ? (v as Handler) : () => structuredClone(v));
  }
}

export function resetTauri() {
  calls.length = 0;
  handlers.clear();
  listeners.clear();
}

/** Daemon calls made through `core`, as "METHOD /path". */
export function coreCalls(): { key: string; body: unknown }[] {
  return calls
    .filter((c) => c.cmd === "core")
    .map((c) => ({ key: `${c.args.method} ${String(c.args.path).split("?")[0]}`, body: c.args.body }));
}

export async function invoke(cmd: string, args: Record<string, unknown> = {}): Promise<unknown> {
  calls.push({ cmd, args });
  const key = cmd === "core" ? `${args.method} ${String(args.path).split("?")[0]}` : cmd;
  const h = handlers.get(key);
  if (!h) throw { error: "not_mocked", message: `no mock for ${key}` };
  return h(args);
}

export async function listen(event: string, cb: (e: { payload: unknown }) => void): Promise<() => void> {
  let set = listeners.get(event);
  if (!set) listeners.set(event, (set = new Set()));
  set.add(cb);
  return () => set!.delete(cb);
}

export function emit(event: string, payload: unknown = null) {
  for (const cb of listeners.get(event) ?? []) cb({ payload });
}
