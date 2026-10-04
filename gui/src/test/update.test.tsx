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

function start(state: CoreState, installUpdate: unknown = "nsis") {
  mockCommands({
    core_status: { state: "running", version: "v0.1.1", restarts: 0 },
    take_pending_links: [],
    "GET /v1/state": state,
    "GET /v1/logs": { lines: [] },
    "GET /v1/update": newer,
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

  it("leaves a system service alone", async () => {
    start(enrolled({ service: { running: true, server: "https://old.example.net", deviceId: "dev_9", deviceName: "nas" } }));
    expect(await screen.findByRole("button", { name: "设置 · 有新版本" })).toBeInTheDocument();
    expect(installs()).toBe(0);
  });
});
