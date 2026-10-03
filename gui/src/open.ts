import { openUrl } from "@tauri-apps/plugin-opener";

/** Opens an http(s) URL in the system browser; other schemes are refused. */
export async function openExternal(url: string): Promise<void> {
  if (!/^https?:\/\//i.test(url)) throw new Error("只能打开 http / https 地址");
  await openUrl(url);
}
