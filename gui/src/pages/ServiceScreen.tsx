import type { ServiceState } from "../api";
import { hostOf } from "../util";

/**
 * Shown when this machine's core runs as a system service (`nyatunnel service install`): the
 * identity lives in the system directory, so the GUI has nothing to enroll or control.
 */
export function ServiceScreen(props: { service: ServiceState }) {
  const s = props.service;
  return (
    <div className="center">
      <div className="empty" style={{ textAlign: "left" }}>
        <div className="big" style={{ textAlign: "center" }}>🛠️</div>
        <h3 style={{ textAlign: "center" }}>本机由系统服务运行</h3>
        <div className="hint" style={{ textAlign: "center" }}>
          NyaTunnel 核心已作为系统服务安装，开机后无需登录即可工作。
        </div>
        <div className="kv" style={{ maxWidth: 460, margin: "18px auto" }}>
          <span>服务器</span><span className="mono">{hostOf(s.server) || "—"}</span>
          <span>设备</span><span>{s.deviceName || "—"}{s.deviceId && <span className="hint mono"> （{s.deviceId}）</span>}</span>
          <span>服务状态</span>
          <span>
            {s.running
              ? <><span className="dot ok" />运行中</>
              : <><span className="dot bad" />已停止</>}
            {s.detail && <span className="hint"> · {s.detail}</span>}
          </span>
        </div>
        <div className="infobox">
          隧道由系统服务管理，可以在服务器的 Web 管理台查看本设备与隧道。
          如需改回由本程序运行，请以管理员身份执行 <code>nyatunnel service uninstall</code>。
        </div>
        <div className="warnbox">
          下发给本机的隧道要在本机确认后才会接通。请以管理员身份执行 <code>nyatunnel tunnels --service</code> 查看，
          用 <code>nyatunnel tunnels confirm &lt;隧道&gt; --service</code> 确认。
        </div>
      </div>
    </div>
  );
}
