import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";

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

export function Modal(props: { title: string; children: ReactNode }) {
  return (
    <div className="modal-bg">
      <div className="modal" role="dialog" aria-modal="true" aria-label={props.title}>
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
    <Modal title={props.title}>
      {props.children}
      <div className="actions">
        <button className="btn" onClick={props.onCancel} disabled={props.busy}>取消</button>
        <button className={props.danger ? "btn d" : "btn p"} onClick={props.onConfirm} disabled={props.busy}>
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
    setTimeout(() => setToasts((ts) => ts.filter((t) => t.id !== id)), 4000);
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
