//go:build darwin

package main

// macOS 剪贴板：直接走系统的 pbcopy / pbpaste（页面自绘右键菜单的复制/粘贴用），
// 跟 Windows 侧一样不依赖浏览器的剪贴板权限。

import (
	"fmt"
	"os/exec"
	"strings"
)

func clipboardGet() (string, error) {
	out, err := exec.Command("pbpaste").Output()
	if err != nil {
		return "", fmt.Errorf("读取剪贴板失败：%w", err)
	}
	return string(out), nil
}

func clipboardSet(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("写入剪贴板失败：%w", err)
	}
	return nil
}
