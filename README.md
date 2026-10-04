# aster-onboard

A small cross-platform desktop tool that points your AI coding agents at an
OpenAI-compatible relay endpoint — in one click, with automatic backups and a
one-click undo.

Instead of hand-editing a different config file (in a different format) for
every agent you use, you paste your API key once, tick the clients you have,
and the tool writes all of them. It works with any OpenAI-compatible endpoint;
it ships with the Aster relay as the default.

[中文说明](#中文说明) · [License](#license) · [Third-party notices](THIRD_PARTY_NOTICES.md)

## What it does

1. **Scans** the machine for the 11 supported clients (desktop apps and CLIs,
   detected by their data directories and `PATH`).
2. **Verifies** your API key by calling `GET /v1/models` on the endpoint you
   entered, and lists the models the endpoint actually serves.
3. **Writes** the endpoint, the key and the full model catalog into each
   selected client's config, creating a backup first.
4. **Undoes** just as easily: *Restore* brings a config back to what this tool
   wrote, *Undo* brings it back to what it was before the tool touched it.

The model list is written in full — you never pick a model in the UI, and the
default model is matched by name in the background.

## Supported clients

| Kind | Clients |
| --- | --- |
| Desktop | Claude Desktop · ZCode · WorkBuddy |
| CLI | Claude Code · Codex · OpenCode · OpenClaw · Hermes Agent · Kimi Code · Grok Build · Antigravity CLI |

## Privacy

- The tool talks **only** to the endpoint you type in. There is no telemetry,
  no analytics, and no update check.
- The local UI server binds `127.0.0.1` on a random port; nothing is exposed
  to the network.
- Your key is kept in memory, written into the clients you selected, and — only
  if you let it — remembered locally in
  `%LOCALAPPDATA%\aster-onboard\settings.json` (Windows) or the equivalent
  application-support directory (macOS). It is never logged and never sent
  anywhere else.

Security note: the local UI API is not authenticated (it is reachable by any
process on the same machine, and `GET /api/settings` returns the remembered
key). That is fine on a personal computer; do not run it on a shared host.

## Windows

Requirements: Windows 10/11 with the **WebView2 Runtime** (preinstalled on
Windows 11 and on any Win10 that has an up-to-date Edge). If it is missing, the
tool says so and offers to open the same UI in your default browser instead.

The app is a single self-contained `.exe` — no installer, no external DLL, no
admin rights (it runs `asInvoker`).

> The build is not code-signed yet. SmartScreen may show "Windows protected
> your PC" on first run — choose *More info → Run anyway*. Some AV engines
> flag freshly built, unsigned binaries that read credentials and write other
> apps' config files; that behaviour is exactly this tool's job, and the
> binaries are built from this repository's source by GitHub Actions.

## macOS

The macOS build has no WebView2 (and Go's native webview bindings require cgo,
so they cannot be cross-compiled). It therefore runs the same UI as a local
server and opens it in a Chromium-based browser as an app window
(`--app=`), falling back to the default browser. Closing that window shuts the
process down.

The `.app` bundles are unsigned and un-notarized, so the first launch needs
**right-click → Open**, or once:

```sh
xattr -dr com.apple.quarantine /Applications/AsterOnboard.app
```

## Build from source

Requires Go (see `go.mod`) and, for the macOS packaging step, Python 3.

```sh
# Windows (also runs the tests)
build.cmd

# macOS .app bundles (cross-compiled from Windows or run on a Mac)
build-mac.cmd
```

The equivalent manual commands:

```sh
# 1. regenerate the Windows resources (icon + manifest + version info)
go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest versioninfo.json
mv resource.syso rsrc_windows_amd64.syso

# 2. build + test
GOOS=windows GOARCH=amd64 go build -p 2 -trimpath -ldflags "-H=windowsgui -s -w" -o aster-onboard.exe .
go test ./...
```

### Resource-file gotchas

`rsrc_windows_amd64.syso` bundles the app icon, the application manifest (DPI
awareness + common controls v6) and the version info. Three things bite:

- All three resources must live in **one** `.rsrc` section — two `.syso` files
  side by side make the Go linker fail with `too many .rsrc sections`.
  `goversioninfo` merges them; `akavel/rsrc` cannot (it has no version-info
  support).
- The `goversioninfo` **CLI only understands `-manifest`**. `-ico` / `-icon` /
  `-o` exist on its library, not on the command, and are silently ignored. Put
  the icon and manifest paths in `versioninfo.json` (`IconPath` /
  `ManifestPath`) instead.
- `-o` is ignored too, so the output is always `./resource.syso`; rename it, or
  the missing `_windows_amd64` suffix makes the Darwin build try to link a
  Windows object file.

## Architecture

Pure Go, no cgo. A single binary with two UI modes sharing one page:

- **Windows** — a borderless Win32 window (`window.go`) hosting WebView2
  (`github.com/wailsapp/go-webview2`). The macOS look is three things working
  together: a frameless window (`WS_POPUP | WS_THICKFRAME`, client area = the
  whole window, rounded via DWM on Win11 and `SetWindowRgn` on Win10); a
  title bar drawn by the page (the red/yellow/green dots); and dragging/resizing
  handed back to the OS — the page posts to `/api/win/action`, and the main
  thread calls `ReleaseCapture` + `WM_NCLBUTTONDOWN(HT*)` so the native
  move/resize loop does the rest.
- **macOS / fallback** — the same local server, opened in a browser window.

The page and the Go side talk over a loopback HTTP API (`/api/scan`,
`/api/models`, `/api/apply`, `/api/restore`, `/api/settings`, `/api/clipboard`,
…). Client definitions live in `clients.go` (detection + config paths),
write plans in `writers.go` (JSON / JSONC / TOML per client), snapshotting and
restore in `backup.go`.

Two details worth knowing if you touch the Windows layer:

- **DPI awareness is mandatory** (declared in `app.manifest`). Without it the
  window is created at 96 DPI on a 150% display and the rendered viewport is
  squashed. Any screenshot/verification script must set
  `SetProcessDpiAwarenessContext(PerMonitorV2)` too, or `GetWindowRect` returns
  virtualized coordinates.
- **The window region must not be set before the corner strategy is decided.**
  Touching `SetWindowRgn` during window creation (before
  `DwmSetWindowAttribute` has been tried) leaves the window permanently clipped
  to its creation-time size — it looks fine until you maximize it.

## Tests

`go test ./...` covers the write plans (each client's exact config shape), the
backup/restore round trip, and the model-catalog/credits pass-through.

## License

MIT — see [LICENSE](LICENSE). Bundled/ported code and dependencies are listed
in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

---

## 中文说明

`aster-onboard` 是一个把 AI 编程客户端（桌面端 + 命令行）一键接到
OpenAI 兼容中转站的小工具：粘贴密钥 → 扫描本机装了哪些客户端 → 勾选 → 一键写入。
写入前自动备份，可「恢复」（还原成本工具写入的样子）或「撤销」（还原成工具动它之前的样子）。

- **支持 11 个客户端**：桌面端 Claude Desktop / ZCode / WorkBuddy；命令行
  Claude Code / Codex / OpenCode / OpenClaw / Hermes Agent / Kimi Code /
  Grok Build / Antigravity CLI。
- **模型不用挑**：中转站 `/v1/models` 的清单全量写入，默认模型按名字自动匹配。
- **只连你填的地址**：没有遥测、没有更新检查；本地界面服务只绑 `127.0.0.1`。
  密钥只在你勾选的客户端配置与本机设置文件里，不进日志、不发往别处。
- **Windows** 需要 WebView2 运行时（Win11 与更新过的 Win10 自带）；缺了会提示并退回浏览器打开同一界面。
  exe 是单文件、免安装、不需要管理员权限。
- **macOS** 没有 WebView2，改用「本地服务 + 浏览器 app 窗口」；`.app` 未签名未公证，
  首次打开要**右键 → 打开**，或执行一次
  `xattr -dr com.apple.quarantine /Applications/AsterOnboard.app`。

构建：Windows 上双击 `build.cmd`（顺带跑测试）；mac 包用 `build-mac.cmd`。
资源文件与两个平台的实现细节见上面的英文章节。
