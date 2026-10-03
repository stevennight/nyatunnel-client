import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Tunnel } from "../api";
import { ToastProvider } from "../components/ui";
import { TunnelDetail } from "../pages/TunnelDetail";
import { TunnelList } from "../pages/TunnelList";
import { enrolled, tunnel } from "./fixtures";
import { coreCalls, mockCommands } from "./tauri";

const locked = tunnel({ id: "internal-api", permissions: { toggle: false, editLocal: false } });
const free = tunnel({
  id: "demo-share",
  display: { accessPolicy: "访问密码", limits: "20 Mbps" }
});
const tcp = tunnel({ id: "ssh-dev", type: "tcp", publicUrl: "t.example.net:22022", localPort: 22, pausedByClient: true });

function renderList(tunnels: Tunnel[], errors: Record<string, string> = {}) {
  const onChanged = vi.fn();
  render(
    <ToastProvider>
      <TunnelList
        state={enrolled({ tunnels, tunnelErrors: errors })}
        onDetail={() => {}}
        onRequest={() => {}}
        onChanged={onChanged}
      />
    </ToastProvider>
  );
  return { onChanged };
}

describe("tunnel list permissions", () => {
  it("disables the switch and shows the lock note when toggle is not allowed", () => {
    renderList([locked, free]);
    const card = screen.getByTestId("tunnel-internal-api");
    expect(within(card).getByRole("switch")).toBeDisabled();
    expect(within(card).getByTestId("lock-note")).toHaveTextContent("不可暂停、不可修改本地地址");

    const other = screen.getByTestId("tunnel-demo-share");
    expect(within(other).getByRole("switch")).toBeEnabled();
    expect(within(other).queryByTestId("lock-note")).toBeNull();
    expect(within(other).getByText(/访问密码 · 20 Mbps/)).toBeInTheDocument();
  });

  it("pauses through the daemon when the switch is allowed", async () => {
    mockCommands({ "POST /v1/tunnels/demo-share": { ok: true } });
    const user = userEvent.setup();
    const { onChanged } = renderList([free]);
    const sw = screen.getByRole("switch", { name: "demo-share 启用开关" });
    expect(sw).toHaveAttribute("aria-checked", "true");
    await user.click(sw);
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(coreCalls()).toEqual([{ key: "POST /v1/tunnels/demo-share", body: { paused: true } }]);
  });

  it("does not call the daemon for a locked switch", async () => {
    const user = userEvent.setup();
    renderList([locked]);
    await user.click(screen.getByRole("switch"));
    expect(coreCalls()).toHaveLength(0);
  });

  it("shows per-tunnel errors, paused state, and offers open only for web addresses", () => {
    renderList([free, tcp], { "demo-share": "dial 127.0.0.1:3000: connection refused" });
    const web = screen.getByTestId("tunnel-demo-share");
    expect(within(web).getByText(/connection refused/)).toBeInTheDocument();
    expect(within(web).getByRole("button", { name: "打开" })).toBeInTheDocument();

    const ssh = screen.getByTestId("tunnel-ssh-dev");
    expect(within(ssh).getByText(/已由你暂停/)).toBeInTheDocument();
    expect(within(ssh).getByRole("switch")).toHaveAttribute("aria-checked", "false");
    expect(within(ssh).queryByRole("button", { name: "打开" })).toBeNull();
  });

  it("disables the switch for a tunnel the administrator turned off", () => {
    renderList([tunnel({ id: "off", enabled: false })]);
    expect(screen.getByRole("switch")).toBeDisabled();
    expect(screen.getByText("已被管理员停用")).toBeInTheDocument();
  });
});

function renderDetail(t: Tunnel) {
  const onChanged = vi.fn();
  render(
    <ToastProvider>
      <TunnelDetail tunnel={t} logs={[]} onBack={() => {}} onChanged={onChanged} />
    </ToastProvider>
  );
  return { onChanged };
}

describe("tunnel detail local target", () => {
  it("hides the editor when editLocal is false", () => {
    renderDetail(locked);
    expect(screen.queryByLabelText("本地地址")).toBeNull();
    expect(screen.queryByLabelText("本地端口")).toBeNull();
    expect(screen.queryByRole("button", { name: "保存" })).toBeNull();
    expect(screen.getByTestId("local-readonly")).toHaveTextContent("127.0.0.1:3000");
  });

  it("edits and sends only the changed fields when editLocal is true", async () => {
    mockCommands({ "POST /v1/tunnels/demo-share": { ok: true } });
    const user = userEvent.setup();
    const { onChanged } = renderDetail(free);
    const port = screen.getByLabelText("本地端口");
    await user.clear(port);
    await user.type(port, "5173");
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(coreCalls()).toEqual([{ key: "POST /v1/tunnels/demo-share", body: { localPort: 5173 } }]);
  });

  it("respects loopbackOnly before sending anything", async () => {
    const user = userEvent.setup();
    renderDetail(tunnel({ id: "lb", permissions: { loopbackOnly: true } }));
    expect(screen.getByText(/只允许本机回环地址（/)).toBeInTheDocument();
    const ip = screen.getByLabelText("本地地址");
    await user.clear(ip);
    await user.type(ip, "192.168.1.20");
    expect(screen.getByText("该隧道只允许本机回环地址")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
    expect(coreCalls()).toHaveLength(0);
  });
});
