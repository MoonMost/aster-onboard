//go:build windows

package main

// Windows 侧的平台钩子：客户端的应用数据目录、打开界面、安装证据。

import (
	"os"
	"os/exec"
	"path/filepath"
)

// appSupportDir 对应 macOS 的 ~/Library/Application Support：
// Windows 上是 %LOCALAPPDATA%（沙箱测试时退回假 home 下的 AppData\Local）。
func appSupportDir(home string) string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" && os.Getenv("ASTER_ONBOARD_HOME") == "" {
		return v
	}
	return filepath.Join(home, "AppData", "Local")
}

func openURL(url string) {
	cmd := exec.Command("cmd", "/c", "start", "", url)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		os.Stderr.WriteString("（打不开浏览器，请手动访问上面的地址）\n")
	}
}

// bundleEvidence Windows 上没有"应用包"概念，桌面端一律看数据目录。
func bundleEvidence(string) []string { return nil }
