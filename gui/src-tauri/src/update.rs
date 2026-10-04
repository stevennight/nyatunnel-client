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

/// Starts the Windows installer passively: no questions, a progress bar, the installed app is
/// replaced in place (our installer never offers to uninstall first) and restarted (`/R`).
#[cfg(windows)]
pub fn run_nsis(installer: &Path) -> Result<(), String> {
    use std::os::windows::process::CommandExt;
    const DETACHED_PROCESS: u32 = 0x0000_0008;
    const CREATE_NEW_PROCESS_GROUP: u32 = 0x0000_0200;
    std::process::Command::new(installer)
        .args(["/P", "/R"])
        .creation_flags(DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP)
        .spawn()
        .map(|_| ())
        .map_err(|e| format!("无法启动安装程序：{e}"))
}

#[cfg(not(windows))]
pub fn run_nsis(_installer: &Path) -> Result<(), String> {
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
