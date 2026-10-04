import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { ToastProvider } from "./components/ui";
import "./styles.css";

if (!import.meta.env.DEV) {
  // This is an app window, not a web page: no 刷新/另存为/打印 menu, and no reload that would drop
  // the current page or a pending enrollment. Text fields and selected text keep the native menu
  // so copy and paste still work.
  document.addEventListener("contextmenu", (e) => {
    const t = e.target as HTMLElement | null;
    const editable = t?.closest("input, textarea, [contenteditable]");
    if (!editable && !window.getSelection()?.toString()) e.preventDefault();
  });
  document.addEventListener("keydown", (e) => {
    const k = e.key.toLowerCase();
    if (e.key === "F5" || ((e.ctrlKey || e.metaKey) && (k === "r" || k === "p"))) e.preventDefault();
  });
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ToastProvider>
      <App />
    </ToastProvider>
  </StrictMode>
);
