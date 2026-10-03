import { useCallback, useEffect, useRef, useState } from "react";
import { listen } from "@tauri-apps/api/event";
import { api, type CoreState, type CoreStatus, type LogLine } from "./api";

/** Status of the bundled core process (pushed by Rust as `core-status`). */
export function useCoreStatus(): CoreStatus | null {
  const [status, setStatus] = useState<CoreStatus | null>(null);
  useEffect(() => {
    let alive = true;
    const un = listen<CoreStatus>("core-status", (e) => alive && setStatus(e.payload));
    api.coreStatus().then((s) => alive && setStatus(s), () => undefined);
    return () => {
      alive = false;
      un.then((f) => f()).catch(() => undefined);
    };
  }, []);
  return status;
}

/** Polls GET /v1/state every `interval` ms while the core is running. */
export function useCoreState(running: boolean, interval = 2000) {
  const [state, setState] = useState<CoreState | null>(null);
  const [error, setError] = useState("");
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const gen = useRef(0);

  const refresh = useCallback(async () => {
    const g = ++gen.current;
    try {
      const st = await api.state();
      if (g === gen.current) {
        setState(st);
        setError("");
      }
    } catch (e) {
      if (g === gen.current) setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    if (!running) return;
    let stopped = false;
    const tick = async () => {
      await refresh();
      if (!stopped) timer.current = setTimeout(tick, interval);
    };
    tick();
    return () => {
      stopped = true;
      clearTimeout(timer.current);
    };
  }, [running, interval, refresh]);

  return { state, setState, error, refresh };
}

const MAX_LINES = 1000;

/** Polls GET /v1/logs incrementally; keeps the most recent lines. */
export function useLogs(running: boolean, interval = 1500) {
  const [lines, setLines] = useState<LogLine[]>([]);
  const after = useRef(0);

  useEffect(() => {
    if (!running) return;
    let stopped = false;
    let t: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      try {
        const r = await api.logs(after.current);
        const got = r.lines ?? [];
        if (got.length > 0) {
          after.current = got[got.length - 1].seq;
          setLines((ls) => [...ls, ...got].slice(-MAX_LINES));
        }
      } catch {
        // The core may be restarting; try again on the next tick.
      }
      if (!stopped) t = setTimeout(tick, interval);
    };
    tick();
    return () => {
      stopped = true;
      clearTimeout(t);
    };
  }, [running, interval]);

  // When the core restarts its sequence numbers restart too.
  useEffect(() => {
    if (!running) after.current = 0;
  }, [running]);

  const clear = useCallback(() => setLines([]), []);
  return { lines, clear };
}

/**
 * Deep links are queued by Rust (cold start and second instances); a `deep-link` event says the
 * queue changed. Links are drained only while `ready`, and are handed to `onLink` one by one.
 */
export function useDeepLinks(ready: boolean, onLink: (url: string) => void) {
  const handler = useRef(onLink);
  handler.current = onLink;

  useEffect(() => {
    if (!ready) return;
    let alive = true;
    const drain = async () => {
      try {
        const urls = await api.takePendingLinks();
        for (const u of urls) if (alive) handler.current(u);
      } catch {
        // ignore; the next event drains again
      }
    };
    const un = listen("deep-link", () => drain());
    drain();
    return () => {
      alive = false;
      un.then((f) => f()).catch(() => undefined);
    };
  }, [ready]);
}
