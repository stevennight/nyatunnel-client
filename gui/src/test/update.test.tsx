import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import type { CoreState } from "../api";
import { ToastProvider } from "../components/ui";
import { enrolled } from "./fixtures";
import { calls, mockCommands } from "./tauri";

const newer = {
  current: "0.1.1",
  latest: "0.1.2",
  newer: true,
  url: "https://github.com/stevennight/nyatunnel-client/releases/tag/v0.1.2"
};

function start(state: CoreState, installUpdate: unknown = "nsis", updateInfo: unknown = newer) {
  mockCommands({
    core_status: { state: "running", version: "v0.1.1", restarts: 0 },
    take_pending_links: [],
    "GET /v1/state": state,
    "GET /v1/logs": { lines: [] },
    "GET /v1/update": updateInfo,
    autostart_status: false,
    install_update: installUpdate
  });
  render(
    <ToastProvider>
      <App />
    </ToastProvider>
  );
}

const installs = () => calls.filter((c) => c.cmd === "install_update").length;

describe("automatic updates", () => {
  beforeEach(() => {
    vi.stubEnv("DEV", false);
    localStorage.clear();
  });
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("downloads and installs a newer release without asking", async () => {
    start(enrolled());
    expect(await screen.findByTestId("installing")).toHaveTextContent("正在下载并安装 NyaTunnel 0.1.2");
    expect(installs()).toBe(1);
  });

  it("offers a retry when installing fails", async () => {
    start(enrolled(), () => {
      throw { error: "update_download_failed", message: "checksum mismatch: refusing to install" };
    });
    expect(await screen.findByText(/自动更新失败：checksum mismatch/)).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "重试" }));
    await waitFor(() => expect(installs()).toBe(2));
  });

  it("waits for the user when automatic updates are off", async () => {
    localStorage.setItem("nyatunnel.autoUpdate", "off");
    start(enrolled());
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "设置 · 有新版本" }));
    expect(screen.getByRole("checkbox", { name: "自动下载并安装新版本" })).not.toBeChecked();
    expect(installs()).toBe(0);

    await user.click(screen.getByRole("button", { name: "立即更新" }));
    expect(await screen.findByTestId("installing")).toBeInTheDocument();
    expect(installs()).toBe(1);
  });

  it("remembers the setting", async () => {
    localStorage.setItem("nyatunnel.autoUpdate", "off");
    start(enrolled());
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "设置 · 有新版本" }));
    await user.click(screen.getByRole("checkbox", { name: "自动下载并安装新版本" }));
    expect(localStorage.getItem("nyatunnel.autoUpdate")).toBe("on");
  });

  it("waits while a system service still runs the app's own core", async () => {
    start(enrolled({ service: { running: true, server: "https://old.example.net", deviceId: "dev_9", deviceName: "nas", sharesCore: true } }));
    expect(await screen.findByRole("button", { name: "设置 · 有新版本" })).toBeInTheDocument();
    expect(installs()).toBe(0);
  });

  it("still updates the app when the service has its own copy", async () => {
    start(enrolled({ service: { running: true, server: "https://old.example.net", deviceId: "dev_9", deviceName: "nas" } }));
    expect(await screen.findByTestId("installing")).toBeInTheDocument();
    expect(installs()).toBe(1);
  });

  it("reports a rolled-back update and does not retry that version for a day", async () => {
    const at = new Date(Date.now() - 60_000).toISOString();
    start(enrolled(), "nsis", {
      ...newer,
      lastAttempt: { from: "0.1.1", to: "0.1.2", ok: false, rolledBack: true, error: "新版本 0.1.2 在 2m0s 内没有启动", at }
    });
    const banner = await screen.findByTestId("update-rolled-back");
    expect(banner).toHaveTextContent("已恢复为 0.1.1");
    expect(installs()).toBe(0);
    await userEvent.setup().click(screen.getByRole("button", { name: "知道了" }));
    await waitFor(() => expect(screen.queryByTestId("update-rolled-back")).toBeNull());
    expect(localStorage.getItem("nyatunnel.updateResultSeen")).toBe(at);
  });

  it("says so once after an unattended update", async () => {
    const at = new Date(Date.now() - 60_000).toISOString();
    start(enrolled(), "nsis", { current: "0.1.2", latest: "0.1.2", newer: false, url: "", lastAttempt: { from: "0.1.1", to: "0.1.2", ok: true, at } });
    expect(await screen.findByText("已自动更新到 0.1.2")).toBeInTheDocument();
    expect(localStorage.getItem("nyatunnel.updateResultSeen")).toBe(at);
  });
});
