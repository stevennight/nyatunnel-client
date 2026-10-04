import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

export function Switch(props: {
  checked: boolean;
  disabled?: boolean;
  label: string;
  title?: string;
  onChange?: (checked: boolean) => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      className="sw"
      aria-checked={props.checked}
      aria-label={props.label}
      title={props.title ?? props.label}
      disabled={props.disabled}
      onClick={() => props.onChange?.(!props.checked)}
    />
  );
}

/** A dialog. Escape calls `onClose` when given (callers leave it out while busy). */
export function Modal(props: { title: string; children: ReactNode; onClose?: () => void }) {
  const panel = useRef<HTMLDivElement>(null);
  const close = useRef(props.onClose);
  close.current = props.onClose;
  useEffect(() => {
    // Move keyboard focus into the dialog (first field, else the dialog itself) so Tab and Enter
    // don't act on the page behind it.
    const el = panel.current;
    if (el && !el.contains(document.activeElement)) {
      (el.querySelector<HTMLElement>("input:not([disabled]), select:not([disabled]), textarea:not([disabled])") ?? el).focus();
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") close.current?.();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  return (
    <div className="modal-bg">
      <div ref={panel} tabIndex={-1} className="modal" role="dialog" aria-modal="true" aria-label={props.title}>
        <h3>{props.title}</h3>
        {props.children}
      </div>
    </div>
  );
}

export function ConfirmDialog(props: {
  title: string;
  children: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <Modal title={props.title} onClose={props.busy ? undefined : props.onCancel}>
      {props.children}
      <div className="actions">
        <button className="btn" onClick={props.onCancel} disabled={props.busy} autoFocus={props.danger}>取消</button>
        <button className={props.danger ? "btn d" : "btn p"} onClick={props.onConfirm} disabled={props.busy} autoFocus={!props.danger}>
          {props.confirmLabel}
        </button>
      </div>
    </Modal>
  );
}

type ToastKind = "ok" | "bad" | "info";
interface Toast { id: number; kind: ToastKind; text: string; }
export type Notify = (text: string, kind?: ToastKind) => void;

const ToastContext = createContext<Notify>(() => {});

export function useNotify(): Notify {
  return useContext(ToastContext);
}

export function ToastProvider(props: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(1);
  const notify = useCallback<Notify>((text, kind = "info") => {
    const id = next.current++;
    setToasts((ts) => [...ts.slice(-3), { id, kind, text }]);
    // Errors stay longer: they are often worth reading twice or copying.
    setTimeout(() => setToasts((ts) => ts.filter((t) => t.id !== id)), kind === "bad" ? 8000 : 4000);
  }, []);
  const value = useMemo(() => notify, [notify]);
  return (
    <ToastContext.Provider value={value}>
      {props.children}
      <div className="toasts" role="status" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className={`toast ${t.kind}`}>{t.text}</div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
