//go:build darwin

package main

// macOS（darwin）适配：这台机器上没有 WebView2/Win32，也没有能在 Windows 上交叉编译的
// 原生 WebView 方案（webview_go / darwinkit 都带 cgo，必须在 Mac 上编）。所以 Mac 版走：
// 本地服务 + 系统浏览器——优先 Chromium 系的 `--app=` 无地址栏窗口（观感最接近原生 App），
// 没有就退回默认浏览器。界面窗口关掉后由页面心跳（/api/ping、/api/bye）通知进程退出。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// winCtl 在 darwin 上没有原生窗口实现：gWin 恒为 nil，
// /api/win/* 于是自动走"网页模式"分支（页面隐藏自绘红黄绿灯，用系统窗口边框）。
// 下面两个方法只为编译对齐：运行时 gWin == nil，根本不会被调到。
type winCtl struct{}

var gWin *winCtl

func (g *winCtl) isActive() bool         { return false }
func (g *winCtl) perform(string, string) {}

// appsUseDark：不注入主题，让页面自己用 prefers-color-scheme 跟随系统外观。
func appsUseDark() bool { return false }

// appSupportDir 与 Windows 的 %LOCALAPPDATA% 对应：~/Library/Application Support。
func appSupportDir(home string) string {
	return filepath.Join(home, "Library", "Application Support")
}

// openURL 优先用 Chromium 系浏览器的 app 模式开窗口（无地址栏），否则退回默认浏览器。
func openURL(url string) {
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Vivaldi.app/Contents/MacOS/Vivaldi",
		"/Applications/Arc.app/Contents/MacOS/Arc",
	}
	for _, app := range candidates {
		if _, err := os.Stat(app); err != nil {
			continue
		}
		cmd := exec.Command(app, "--app="+url, "--window-size=1120,780", "--no-first-run", "--no-default-browser-check")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if cmd.Start() == nil {
			return
		}
	}
	_ = exec.Command("open", url).Start()
}

// runGUI：起本地服务 → 开窗口 → 等页面消失就退出。
func runGUI() {
	url, err := startServer(0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "起服务失败：", err)
		os.Exit(1)
	}
	fmt.Println("aster-onboard // 中转站接入导入器")
	fmt.Println("界面地址：", url)
	fmt.Println("界面窗口关掉后本程序会自动退出；也可以直接把上面的地址贴进任意浏览器。")
	openURL(url)
	waitForPageGone()
}

// bundleEvidence macOS：桌面端装没装，可以看 /Applications 里的 .app；
// CLI 客户端仍然靠 ~/.xxx 与 PATH（下面这些路径不存在就不会算作证据）。
func bundleEvidence(clientID string) []string {
	apps := map[string][]string{
		"claude-desktop": {"/Applications/Claude.app"},
		"zcode":          {"/Applications/ZCode.app"},
		"workbuddy":      {"/Applications/WorkBuddy.app"},
		"antigravity":    {"/Applications/Antigravity.app"},
		"kimi":           {"/Applications/Kimi.app"},
		"grok":           {"/Applications/Grok.app"},
	}
	return exists(apps[clientID]...)
}
