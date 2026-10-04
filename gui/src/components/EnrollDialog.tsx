import { useEffect, useState } from "react";
import { api, type CoreState, type EnrollPreview } from "../api";
import { errorMessage, formatTime, hostOf } from "../util";
import { Modal } from "./ui";

export interface EnrollRequest {
  server: string;
  code: string;
  /** Set by the daemon when a deep link points at a different server than the enrolled one. */
  switchServer?: boolean;
  /** Where the request came from; deep links are shown with an extra caution line. */
  source: "link" | "form";
}

/**
 * Registration confirmation. It only *reads* a preview from the server; the device is enrolled
 * (key generated, code claimed) exclusively when the user presses 确认加入.
 */
export function EnrollDialog(props: {
  request: EnrollRequest;
  current: CoreState | null;
  onCancel: () => void;
  onDone: (state: CoreState) => void;
}) {
  const { request, current } = props;
  const [preview, setPreview] = useState<EnrollPreview | null>(null);
  const [server, setServer] = useState(request.server);
  const [loadError, setLoadError] = useState("");
  const [name, setName] = useState("");
  // A first enroll offers autostart; a re-enroll or server switch keeps whatever the user chose.
  const [autostart, setAutostart] = useState(!current?.enrolled);
  const [autostartWas, setAutostartWas] = useState(false);
  const [busy, setBusy] = useState(false);
  const [submitError, setSubmitError] = useState("");

  useEffect(() => {
    let cancelled = false;
    setPreview(null);
    setLoadError("");
    api.preview(request.server, request.code).then(
      (r) => {
        if (cancelled) return;
        setPreview(r.preview);
        setServer(r.server);
        setName(r.preview.deviceNameHint || "");
      },
      (e) => !cancelled && setLoadError(errorMessage(e))
    );
    return () => {
      cancelled = true;
    };
  }, [request.server, request.code]);

  useEffect(() => {
    if (!current?.enrolled) return;
    let cancelled = false;
    api.autostartEnabled().then(
      (on) => {
        if (cancelled) return;
        setAutostart(on);
        setAutostartWas(on);
      },
      () => undefined
    );
    return () => {
      cancelled = true;
    };
    // Read once when the dialog opens.
  }, []);

  const host = hostOf(server);
  const enrolledHost = current?.enrolled ? hostOf(current.server) : "";
  const switching = Boolean(request.switchServer) || (enrolledHost !== "" && enrolledHost !== host);
  const reenroll = enrolledHost !== "" && !switching;
  const secure = server.startsWith("https://");

  async function confirm() {
    setBusy(true);
    setSubmitError("");
    try {
      const st = await api.enroll(server, request.code, name.trim());
      if (autostart !== autostartWas) {
        await api.setAutostart(autostart).catch(() => undefined);
      }
      props.onDone(st);
    } catch (e) {
      setSubmitError(errorMessage(e));
      setBusy(false);
    }
  }

  return (
    <Modal title="加入 NyaTunnel 服务器？" onClose={busy ? undefined : props.onCancel}>
      {!preview && !loadError && <div className="hint">正在向 {host || "服务器"} 获取邀请信息…</div>}
      {loadError && (
        <>
          <div className="badbox">无法获取邀请信息：{loadError}</div>
          <div className="kv">
            <span>服务器</span><span className="mono">{host}</span>
            <span>注册码</span><span className="mono">{request.code}</span>
          </div>
        </>
      )}
      {preview && (
        <>
          <div className="kv">
            <span>服务器</span>
            <span>
              <b className="mono" data-testid="enroll-host">{host}</b>{" "}
              {secure ? <span className="tag ok">TLS 有效</span> : <span className="tag bad">未加密</span>}
            </span>
            <span>服务器名称</span><span>{preview.serverName || "—"}</span>
            <span>邀请人</span><span data-testid="enroll-owner">{preview.owner || "—"}</span>
            <span>本机名称</span>
            <span>
              <input
                className="inp"
                aria-label="本机名称"
                value={name}
                maxLength={64}
                placeholder="默认使用主机名"
                onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && !busy && void confirm()}
                style={{ width: 220 }}
              />
            </span>
            <span>将获得的隧道</span>
            <span>
              {preview.tunnels && preview.tunnels.length > 0
                ? preview.tunnels.map((t) => (
                    <div key={t.name + t.publicUrl}>
                      <code>{t.name}</code> → <span className="mono">{t.publicUrl}</span>
                    </div>
                  ))
                : <span className="hint">暂无（管理员之后分配的隧道会自动出现）</span>}
            </span>
            {preview.expiresAt > 0 && (
              <>
                <span>注册码有效期</span>
                <span>{formatTime(new Date(preview.expiresAt * 1000).toISOString())} 前</span>
              </>
            )}
          </div>
          {switching ? (
            <div className="badbox" data-testid="switch-warning">
              ⚠ 本机当前已注册到 <b className="mono">{enrolledHost || "另一台服务器"}</b>。确认后将切换到{" "}
              <b className="mono">{host}</b>，并删除当前服务器的设备密钥，原有隧道会停止工作。
            </div>
          ) : reenroll ? (
            <div className="infobox">本机已注册到此服务器。确认后将以新的设备密钥重新注册，并替换现有密钥。</div>
          ) : (
            <div className="warnbox" data-testid="first-time-warning">
              ⚠ 首次连接此服务器。请确认这是你信任的管理员发给你的{request.source === "link" ? "链接" : "注册码"}。
              注册后，管理员可以通过此客户端把本机端口暴露到公网。
            </div>
          )}
          {request.source === "link" && (
            <div className="hint">此请求来自一个 nyatunnel:// 链接。如果你没有点击过邀请链接，请取消。</div>
          )}
          <label className="chk" style={{ marginTop: 10 }}>
            <input type="checkbox" checked={autostart} onChange={(e) => setAutostart(e.target.checked)} />
            注册后开机自动启动并连接
          </label>
        </>
      )}
      {submitError && <div className="err">注册失败：{submitError}</div>}
      <div className="actions">
        <button className="btn" onClick={props.onCancel} disabled={busy}>取消</button>
        <button className="btn p" onClick={confirm} disabled={!preview || busy}>
          {busy ? "正在注册…" : switching ? "确认切换并加入" : "确认加入"}
        </button>
      </div>
    </Modal>
  );
}
