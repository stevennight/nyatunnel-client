//! Installing an update that the core downloaded and verified (SHA256SUMS of the GitHub release).
//!
//! The installer path comes from the core, never from the web view, and is only accepted when it
//! is a NyaTunnel installer inside the core's update directory.

use std::path::{Path, PathBuf};

/// The directory the core downloads installers into (`update.UpdateDir()` on the Go side).
fn update_dir() -> PathBuf {
    std::env::temp_dir().join("NyaTunnel-update")
}

/// Checks that `path` is a downloaded installer of `kind` and returns its canonical form.
pub fn validate(path: &str, kind: &str) -> Result<PathBuf, String> {
    let ext = match kind {
        "nsis" => "exe",
        "dmg" => "dmg",
        "appimage" => "AppImage",
        "deb" => "deb",
        _ => return Err(format!("未知的安装包类型 {kind}")),
    };
    let file = Path::new(path)
        .canonicalize()
        .map_err(|e| format!("找不到安装包：{e}"))?;
    let dir = update_dir()
        .canonicalize()
        .map_err(|e| format!("找不到更新目录：{e}"))?;
    let name = file
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or_default();
    let ext_ok = file.extension().and_then(|e| e.to_str()) == Some(ext);
    if file.parent() != Some(dir.as_path()) || !name.starts_with("NyaTunnel_") || !ext_ok {
        return Err("安装包路径不可信，已拒绝".into());
    }
    Ok(file)
}

/// Hands a Windows update to the unattended updater (`nyatunnel update apply --mode gui`): a copy
/// of the bundled core started detached from outside the install directory. Once this app has
/// exited it installs silently, starts the new version in the tray and checks that it runs (and
/// reconnects, if it was connected); otherwise it restores the old files and starts the old
/// version. The machine is never left without a running tunnel client.
#[cfg(windows)]
pub fn launch_updater(installer: &Path, from: &str, to: &str) -> Result<(), String> {
    use std::os::windows::process::CommandExt;
    const DETACHED_PROCESS: u32 = 0x0000_0008;
    const CREATE_NEW_PROCESS_GROUP: u32 = 0x0000_0200;
    const CREATE_NO_WINDOW: u32 = 0x0800_0000;
    const CREATE_BREAKAWAY_FROM_JOB: u32 = 0x0100_0000;

    let exe = std::env::current_exe().map_err(|e| format!("找不到程序位置：{e}"))?;
    let app_dir = exe.parent().ok_or("找不到安装目录")?;
    // The updater stops and backs up everything in this directory: only accept an installation.
    if !app_dir.join("uninstall.exe").is_file() {
        return Err("当前不是安装版（找不到 uninstall.exe），请从发布页下载安装包更新".into());
    }
    let app = exe
        .file_name()
        .and_then(|n| n.to_str())
        .ok_or("程序文件名无效")?;
    let updater = update_dir().join("nyatunnel-updater.exe");
    std::fs::copy(app_dir.join("nyatunnel.exe"), &updater)
        .map_err(|e| format!("无法准备更新程序（可能已有更新在进行）：{e}"))?;

    let spawn = |flags: u32| {
        std::process::Command::new(&updater)
            .args(["update", "apply", "--mode", "gui", "--installer"])
            .arg(installer)
            .arg("--app-dir")
            .arg(app_dir)
            .args(["--app", app, "--from", from, "--to", to, "--wait-pid"])
            .arg(std::process::id().to_string())
            .creation_flags(flags)
            .spawn()
    };
    let base = DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW;
    // Break away from any job object so the updater survives this app exiting.
    spawn(base | CREATE_BREAKAWAY_FROM_JOB)
        .or_else(|_| spawn(base))
        .map(|_| ())
        .map_err(|e| format!("无法启动更新程序：{e}"))
}

#[cfg(not(windows))]
pub fn launch_updater(_installer: &Path, _from: &str, _to: &str) -> Result<(), String> {
    Err("此平台不使用 Windows 安装程序".into())
}

/// Replaces the running AppImage ($APPIMAGE) with the downloaded one and starts it.
#[cfg(target_os = "linux")]
pub fn replace_appimage(downloaded: &Path) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;
    let target = std::env::var_os("APPIMAGE").ok_or("当前不是以 AppImage 方式运行")?;
    let target = PathBuf::from(target);
    let tmp = target.with_extension("AppImage.new");
    std::fs::copy(downloaded, &tmp).map_err(|e| format!("无法写入新版本：{e}"))?;
    std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o755))
        .map_err(|e| e.to_string())?;
    std::fs::rename(&tmp, &target).map_err(|e| format!("无法替换旧版本：{e}"))?;
    std::process::Command::new(&target)
        .spawn()
        .map(|_| ())
        .map_err(|e| format!("无法启动新版本：{e}"))
}

#[cfg(not(target_os = "linux"))]
pub fn replace_appimage(_downloaded: &Path) -> Result<(), String> {
    Err("此平台不使用 AppImage".into())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accepts_only_installers_in_the_update_dir() {
        let dir = update_dir();
        std::fs::create_dir_all(&dir).unwrap();
        let good = dir.join("NyaTunnel_9.9.9_windows_x64.exe");
        std::fs::write(&good, b"x").unwrap();
        assert!(validate(good.to_str().unwrap(), "nsis").is_ok());
        assert!(
            validate(good.to_str().unwrap(), "dmg").is_err(),
            "kind must match the extension"
        );

        let renamed = dir.join("evil.exe");
        std::fs::write(&renamed, b"x").unwrap();
        assert!(validate(renamed.to_str().unwrap(), "nsis").is_err());

        let outside = std::env::temp_dir().join("NyaTunnel_9.9.9_windows_x64.exe");
        std::fs::write(&outside, b"x").unwrap();
        assert!(validate(outside.to_str().unwrap(), "nsis").is_err());

        let sneaky = dir.join("..").join("NyaTunnel_9.9.9_windows_x64.exe");
        assert!(validate(sneaky.to_str().unwrap(), "nsis").is_err());

        for p in [good, renamed, outside] {
            let _ = std::fs::remove_file(p);
        }
    }
}
