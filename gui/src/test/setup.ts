import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";
import { resetTauri } from "./tauri";

vi.mock("@tauri-apps/api/core", async () => {
  const m = await import("./tauri");
  return { invoke: m.invoke };
});
vi.mock("@tauri-apps/api/event", async () => {
  const m = await import("./tauri");
  return { listen: m.listen };
});
vi.mock("@tauri-apps/api/app", () => ({ getVersion: async () => "0.1.0" }));
vi.mock("@tauri-apps/plugin-opener", () => ({ openUrl: vi.fn(async () => undefined) }));

afterEach(() => {
  cleanup();
  resetTauri();
});
