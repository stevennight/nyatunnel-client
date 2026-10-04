//! Supervises the bundled Go core (`nyatunnel daemon`) and proxies HTTP calls to it.
//!
//! The core is started as a Tauri sidecar with a fresh random token in `NYATUNNEL_IPC_TOKEN`. It
//! announces its loopback address on stdout; afterwards every call from the web view goes through
//! [`Core::request`], which adds the token. The token never reaches JavaScript.

use std::collections::VecDeque;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use tauri::{AppHandle, Emitter};
use tauri_plugin_shell::process::{CommandChild, CommandEvent};
use tauri_plugin_shell::ShellExt;

const READY_TIMEOUT: Duration = Duration::from_secs(20);
const REQUEST_TIMEOUT: Duration = Duration::from_secs(45);
const MAX_BACKOFF: Duration = Duration::from_secs(30);
/// A core that ran at least this long is considered healthy; the next restart starts fast again.
const HEALTHY_RUN: Duration = Duration::from_secs(60);

#[derive(Clone, Serialize, Debug)]
#[serde(rename_all = "camelCase")]
pub struct CoreStatus {
    /// "starting" | "running" | "error" | "stopped"
    pub state: &'static str,
    pub version: Option<String>,
    pub error: Option<String>,
    pub restarts: u32,
}

/// Error returned to the web view; daemon errors (`{error, message}`) are passed through.
#[derive(Serialize, Debug)]
pub struct CoreError {
    pub error: String,
    pub message: String,
    pub status: u16,
}

impl CoreError {
    pub fn new(error: &str, message: impl Into<String>) -> Self {
        Self {
            error: error.into(),
            message: message.into(),
            status: 0,
        }
    }
}

#[derive(Clone)]
struct Conn {
    addr: String,
    token: String,
}

struct Inner {
    conn: Option<Conn>,
    child: Option<CommandChild>,
    status: CoreStatus,
    stopping: bool,
}

pub struct Core {
    inner: Mutex<Inner>,
    client: reqwest::Client,
}

#[derive(Deserialize)]
struct Ready {
    event: String,
    addr: String,
    #[serde(default)]
    version: String,
}

impl Core {
    pub fn new() -> Arc<Self> {
        let client = reqwest::Client::builder()
            .no_proxy()
            .timeout(REQUEST_TIMEOUT)
            .build()
            .expect("http client");
        Arc::new(Self {
            inner: Mutex::new(Inner {
                conn: None,
                child: None,
                status: CoreStatus {
                    state: "starting",
                    version: None,
                    error: None,
                    restarts: 0,
                },
                stopping: false,
            }),
            client,
        })
    }

    pub fn status(&self) -> CoreStatus {
        self.inner.lock().unwrap().status.clone()
    }

    fn set_status(&self, app: &AppHandle, f: impl FnOnce(&mut CoreStatus)) {
        let status = {
            let mut inner = self.inner.lock().unwrap();
            f(&mut inner.status);
            inner.status.clone()
        };
        let _ = app.emit("core-status", status);
    }

    fn stopping(&self) -> bool {
        self.inner.lock().unwrap().stopping
    }

    /// Runs the core and restarts it with exponential backoff until [`Core::shutdown`].
    pub async fn supervise(self: Arc<Self>, app: AppHandle) {
        let mut backoff = Duration::from_secs(1);
        let mut restarts = 0u32;
        loop {
            if self.stopping() {
                break;
            }
            self.set_status(&app, |s| {
                s.state = "starting";
                s.restarts = restarts;
            });
            let started = Instant::now();
            let error = self.run_once(&app).await;
            if self.stopping() {
                break;
            }
            if started.elapsed() >= HEALTHY_RUN {
                backoff = Duration::from_secs(1);
            }
            restarts += 1;
            log::warn(&format!("core stopped: {error}; restarting in {backoff:?}"));
            self.set_status(&app, |s| {
                s.state = "error";
                s.error = Some(error);
                s.restarts = restarts;
            });
            tokio::time::sleep(backoff).await;
            backoff = (backoff * 2).min(MAX_BACKOFF);
        }
        self.set_status(&app, |s| s.state = "stopped");
    }

    /// Starts the core once and waits until it exits. Returns why it stopped.
    async fn run_once(&self, app: &AppHandle) -> String {
        let token = random_token();
        let spawned = app.shell().sidecar("nyatunnel").and_then(|cmd| {
            cmd.args(["daemon", "--gui", "--exit-with-stdin"])
                .env("NYATUNNEL_IPC_TOKEN", &token)
                .spawn()
        });
        let (mut rx, child) = match spawned {
            Ok(v) => v,
            Err(e) => return format!("无法启动核心：{e}"),
        };
        {
            let mut inner = self.inner.lock().unwrap();
            if inner.stopping {
                let _ = child.kill();
                return "正在退出".into();
            }
            inner.child = Some(child);
        }

        let mut ready = false;
        let mut tail: VecDeque<String> = VecDeque::new();
        let deadline = tokio::time::sleep(READY_TIMEOUT);
        tokio::pin!(deadline);
        let mut code = None;
        let mut timed_out = false;
        loop {
            tokio::select! {
                ev = rx.recv() => match ev {
                    Some(CommandEvent::Stdout(line)) => {
                        let line = String::from_utf8_lossy(&line);
                        if !ready {
                            if let Ok(r) = serde_json::from_str::<Ready>(line.trim()) {
                                if r.event == "ready" && is_loopback_addr(&r.addr) {
                                    ready = true;
                                    self.inner.lock().unwrap().conn =
                                        Some(Conn { addr: r.addr, token: token.clone() });
                                    let version = Some(r.version).filter(|v| !v.is_empty());
                                    self.set_status(app, |s| {
                                        s.state = "running";
                                        s.version = version;
                                        s.error = None;
                                    });
                                }
                            }
                        }
                    }
                    Some(CommandEvent::Stderr(line)) => {
                        let line = String::from_utf8_lossy(&line).trim_end().to_string();
                        if !line.is_empty() {
                            log::core(&line);
                            if tail.len() == 5 {
                                tail.pop_front();
                            }
                            tail.push_back(line);
                        }
                    }
                    Some(CommandEvent::Terminated(p)) => {
                        code = p.code;
                        break;
                    }
                    Some(CommandEvent::Error(e)) => tail.push_back(e),
                    Some(_) => {}
                    None => break,
                },
                _ = &mut deadline, if !ready && !timed_out => {
                    timed_out = true;
                    tail.push_back("核心启动超时".into());
                    if let Some(child) = self.inner.lock().unwrap().child.take() {
                        let _ = child.kill();
                    }
                }
            }
        }
        {
            let mut inner = self.inner.lock().unwrap();
            inner.conn = None;
            inner.child = None;
        }
        let last = tail.back().cloned().unwrap_or_default();
        match code {
            Some(c) if last.is_empty() => format!("核心进程已退出（代码 {c}）"),
            Some(c) => format!("核心进程已退出（代码 {c}）：{last}"),
            None if last.is_empty() => "核心进程已退出".into(),
            None => format!("核心进程已退出：{last}"),
        }
    }

    /// Calls the daemon. Only `/v1/...` paths and GET/POST are allowed.
    pub async fn request(
        &self,
        method: &str,
        path: &str,
        body: Option<Value>,
    ) -> Result<Value, CoreError> {
        self.request_timeout(method, path, body, REQUEST_TIMEOUT)
            .await
    }

    /// Like `request`, with a custom timeout (downloads take longer than the default).
    pub async fn request_timeout(
        &self,
        method: &str,
        path: &str,
        body: Option<Value>,
        timeout: Duration,
    ) -> Result<Value, CoreError> {
        if !valid_path(path) {
            return Err(CoreError::new("bad_request", "无效的请求路径"));
        }
        let conn = self.inner.lock().unwrap().conn.clone();
        let Some(conn) = conn else {
            return Err(CoreError::new("core_unavailable", "核心服务未运行"));
        };
        let url = format!("http://{}{}", conn.addr, path);
        let req = match method {
            "GET" => self.client.get(&url),
            "POST" => self
                .client
                .post(&url)
                .header("Content-Type", "application/json")
                .body(
                    serde_json::to_vec(&body.unwrap_or_else(|| Value::Object(Default::default())))
                        .unwrap_or_default(),
                ),
            _ => return Err(CoreError::new("bad_request", "不支持的请求方法")),
        };
        let resp = req
            .timeout(timeout)
            .bearer_auth(&conn.token)
            .send()
            .await
            .map_err(|e| CoreError::new("core_unavailable", format!("无法连接核心服务：{e}")))?;
        let status = resp.status();
        let bytes = resp
            .bytes()
            .await
            .map_err(|e| CoreError::new("core_unavailable", format!("读取核心响应失败：{e}")))?;
        let value: Value = if bytes.is_empty() {
            Value::Null
        } else {
            serde_json::from_slice(&bytes).unwrap_or(Value::Null)
        };
        if status.is_success() {
            return Ok(value);
        }
        let field = |k: &str| {
            value
                .get(k)
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_string()
        };
        let mut error = field("error");
        let mut message = field("message");
        if error.is_empty() {
            error = format!("http_{}", status.as_u16());
        }
        if message.is_empty() {
            message = match status.as_u16() {
                401 | 403 => "核心服务拒绝了请求".into(),
                404 => "核心不支持此操作，请更新 NyaTunnel".into(),
                _ => format!("核心服务返回错误（{}）", status.as_u16()),
            };
        }
        Err(CoreError {
            error,
            message,
            status: status.as_u16(),
        })
    }

    /// Asks the core to stop, then kills it if it is still running. Blocks for up to ~3.5 s.
    pub fn shutdown(&self) {
        let conn = {
            let mut inner = self.inner.lock().unwrap();
            inner.stopping = true;
            inner.conn.take()
        };
        if let Some(conn) = conn {
            let client = self.client.clone();
            let _ = tauri::async_runtime::block_on(async move {
                client
                    .post(format!("http://{}/v1/shutdown", conn.addr))
                    .bearer_auth(&conn.token)
                    .header("Content-Type", "application/json")
                    .body("{}")
                    .timeout(Duration::from_millis(1500))
                    .send()
                    .await
            });
            // The supervisor clears `child` once the process has exited.
            let until = Instant::now() + Duration::from_secs(3);
            while Instant::now() < until && self.inner.lock().unwrap().child.is_some() {
                std::thread::sleep(Duration::from_millis(50));
            }
        }
        if let Some(child) = self.inner.lock().unwrap().child.take() {
            let _ = child.kill();
        }
    }
}

fn random_token() -> String {
    let mut buf = [0u8; 32];
    getrandom::fill(&mut buf).expect("system randomness");
    buf.iter().map(|b| format!("{b:02x}")).collect()
}

fn is_loopback_addr(addr: &str) -> bool {
    addr.parse::<std::net::SocketAddr>()
        .map(|a| a.ip().is_loopback())
        .unwrap_or(false)
}

fn valid_path(path: &str) -> bool {
    path.starts_with("/v1/")
        && !path.contains("..")
        && !path.contains('#')
        && !path.contains('\\')
        && path.chars().all(|c| c.is_ascii_graphic())
}

pub(crate) mod log {
    pub fn warn(msg: &str) {
        eprintln!("[nyatunnel-gui] {msg}");
    }
    pub fn core(line: &str) {
        if cfg!(debug_assertions) {
            eprintln!("[core] {line}");
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn paths_are_restricted_to_the_api() {
        assert!(valid_path("/v1/state"));
        assert!(valid_path("/v1/logs?after=12"));
        assert!(valid_path("/v1/tunnels/t_1"));
        assert!(!valid_path("/v2/state"));
        assert!(!valid_path("/v1/../debug"));
        assert!(!valid_path("http://evil/v1/state"));
        assert!(!valid_path("/v1/state with space"));
    }

    #[test]
    fn ready_address_must_be_loopback() {
        assert!(is_loopback_addr("127.0.0.1:5000"));
        assert!(is_loopback_addr("[::1]:5000"));
        assert!(!is_loopback_addr("0.0.0.0:5000"));
        assert!(!is_loopback_addr("10.0.0.1:5000"));
    }

    #[test]
    fn tokens_are_long_hex() {
        let t = random_token();
        assert_eq!(t.len(), 64);
        assert!(t.chars().all(|c| c.is_ascii_hexdigit()));
        assert_ne!(t, random_token());
    }
}
