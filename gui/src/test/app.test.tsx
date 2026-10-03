import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { App } from "../App";
import type { CoreState } from "../api";
import { ToastProvider } from "../components/ui";
import { enrolled, notEnrolled } from "./fixtures";
import { coreCalls, mockCommands } from "./tauri";

function start(state: CoreState, update: unknown = { current: "v0.1.0", latest: "v0.1.0", newer: false, url: "" }) {
  mockCommands({
    core_status: { state: "running", version: "v0.1.0", restarts: 0 },
    take_pending_links: [],
    "GET /v1/state": state,
    "GET /v1/logs": { lines: [] },
    "GET /v1/update": update,
    autostart_status: false
  });
  render(
    <ToastProvider>
      <App />
    </ToastProvider>
  );
}

describe("app shell", () => {
  it("shows the system service screen instead of enrollment", async () => {
    start({
      ...notEnrolled,
      service: { running: true, server: "https://tunnel.example.net", deviceId: "dev_9", deviceName: "nas" }
    });
    expect(await screen.findByText("本机由系统服务运行")).toBeInTheDocument();
    expect(screen.getByText("tunnel.example.net")).toBeInTheDocument();
    expect(screen.getByText("运行中")).toBeInTheDocument();
    expect(screen.getByText(/nyatunnel service uninstall/)).toBeInTheDocument();
    expect(screen.queryByLabelText("注册码")).toBeNull();
  });

  it("shows the enrollment form when not enrolled", async () => {
    start(notEnrolled);
    expect(await screen.findByLabelText("注册码")).toBeInTheDocument();
  });

  it("shows transport and a quietly found update", async () => {
    start(enrolled({ transport: "proxy" }), {
      current: "v0.1.0",
      latest: "v0.2.0",
      newer: true,
      url: "https://github.com/stevennight/nyatunnel-client/releases/tag/v0.2.0"
    });
    expect(await screen.findByText(/已连接 · 经 443/)).toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "设置 · 有新版本" }));
    expect(await screen.findByTestId("update-available")).toHaveTextContent("发现新版本 0.2.0");
    await waitFor(() => expect(coreCalls().filter((c) => c.key === "GET /v1/update")).toHaveLength(1));
  });

  it("shows a banner when the core is restarting", async () => {
    mockCommands({ core_status: { state: "error", error: "核心进程已退出（代码 1）", restarts: 2 } });
    render(
      <ToastProvider>
        <App />
      </ToastProvider>
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("第 2 次");
  });
});
