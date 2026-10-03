import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { App } from "../App";
import { ToastProvider } from "../components/ui";
import type { CoreState } from "../api";
import { enrolled, notEnrolled } from "./fixtures";
import { calls, coreCalls, emit, mockCommands } from "./tauri";

const LINK = "nyatunnel://enroll?v=1&s=tunnel.example.net&c=K7QP-3XMD";

const preview = {
  preview: {
    serverName: "Nya 的家庭网络",
    owner: "admin",
    deviceNameHint: "dev-laptop",
    tunnels: [{ name: "demo-share", publicUrl: "https://demo-7f3a.t.example.net" }],
    expiresAt: 1_900_000_000
  },
  server: "https://tunnel.example.net"
};

function setup(state: CoreState, link: Record<string, unknown>, links = [LINK]) {
  let pending = [...links];
  let current = state;
  mockCommands({
    core_status: { state: "running", version: "v0.1.0", restarts: 0 },
    take_pending_links: () => {
      const out = pending;
      pending = [];
      return out;
    },
    "GET /v1/state": () => current,
    "GET /v1/logs": { lines: [] },
    "GET /v1/update": { current: "v0.1.0", latest: "v0.1.0", newer: false, url: "" },
    "POST /v1/link": link,
    "POST /v1/enroll/preview": preview,
    "POST /v1/enroll": () => {
      current = enrolled({ server: "https://tunnel.example.net" });
      return current;
    },
    autostart_set: true
  });
  return {
    push(url: string) {
      pending.push(url);
      emit("deep-link", [url]);
    }
  };
}

function renderApp() {
  return render(
    <ToastProvider>
      <App />
    </ToastProvider>
  );
}

const enrollCalls = () => coreCalls().filter((c) => c.key === "POST /v1/enroll");

describe("deep link enrollment", () => {
  it("shows the server, owner and tunnels and does nothing until confirmed", async () => {
    setup(notEnrolled, { kind: "enroll", server: "https://tunnel.example.net", code: "K7QP-3XMD", switchServer: false });
    const user = userEvent.setup();
    renderApp();

    const dialog = await screen.findByRole("dialog", { name: "加入 NyaTunnel 服务器？" });
    expect(await within(dialog).findByTestId("enroll-host")).toHaveTextContent("tunnel.example.net");
    expect(within(dialog).getByTestId("enroll-owner")).toHaveTextContent("admin");
    expect(within(dialog).getByText("demo-share")).toBeInTheDocument();
    expect(within(dialog).getByTestId("first-time-warning")).toHaveTextContent("首次连接此服务器");
    expect(within(dialog).queryByTestId("switch-warning")).toBeNull();

    // Only the link was parsed and the preview fetched; nothing was claimed.
    expect(coreCalls().map((c) => c.key)).toContain("POST /v1/enroll/preview");
    expect(enrollCalls()).toHaveLength(0);
    expect(calls.some((c) => c.cmd === "autostart_set")).toBe(false);

    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(enrollCalls()).toHaveLength(0);
    expect(calls.some((c) => c.cmd === "autostart_set")).toBe(false);
  });

  it("enrolls with the chosen device name only after 确认加入", async () => {
    setup(notEnrolled, { kind: "enroll", server: "https://tunnel.example.net", code: "K7QP-3XMD", switchServer: false });
    const user = userEvent.setup();
    renderApp();

    const dialog = await screen.findByRole("dialog");
    const name = await within(dialog).findByLabelText("本机名称");
    await user.clear(name);
    await user.type(name, "office-pc");
    expect(enrollCalls()).toHaveLength(0);

    await user.click(within(dialog).getByRole("button", { name: "确认加入" }));
    await waitFor(() => expect(enrollCalls()).toHaveLength(1));
    expect(enrollCalls()[0].body).toEqual({ server: "https://tunnel.example.net", code: "K7QP-3XMD", name: "office-pc" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(await screen.findByRole("heading", { name: "我的隧道" })).toBeInTheDocument();
  });

  it("warns when the link would switch to another server", async () => {
    setup(enrolled(), { kind: "enroll", server: "https://tunnel.example.net", code: "K7QP-3XMD", switchServer: true });
    renderApp();

    const dialog = await screen.findByRole("dialog");
    const warning = await within(dialog).findByTestId("switch-warning");
    expect(warning).toHaveTextContent("old.example.net");
    expect(warning).toHaveTextContent("tunnel.example.net");
    expect(within(dialog).queryByTestId("first-time-warning")).toBeNull();
    expect(within(dialog).getByRole("button", { name: "确认切换并加入" })).toBeEnabled();
    expect(enrollCalls()).toHaveLength(0);
  });

  it("ignores a second link while a confirmation is open", async () => {
    const h = setup(notEnrolled, { kind: "enroll", server: "https://tunnel.example.net", code: "K7QP-3XMD", switchServer: false });
    renderApp();
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByTestId("enroll-host");

    h.push("nyatunnel://enroll?v=1&s=evil.example.org&c=AAAA-BBBB");
    expect(await screen.findByText(/新的链接已忽略/)).toBeInTheDocument();
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(within(dialog).getByTestId("enroll-host")).toHaveTextContent("tunnel.example.net");
    expect(enrollCalls()).toHaveLength(0);
  });

  it("ignores open links for another server", async () => {
    setup(enrolled(), { kind: "open", server: "https://tunnel.example.net", tunnelId: "t1", switchServer: true }, [
      "nyatunnel://open?s=tunnel.example.net&t=t1"
    ]);
    renderApp();
    expect(await screen.findByText(/不是本机当前的服务器，已忽略/)).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
