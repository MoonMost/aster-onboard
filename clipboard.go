//go:build windows

package main

// 系统剪贴板读写：页面自绘右键菜单的「复制/剪切/粘贴」走这里。
//
// 不用 Chromium 自己的剪贴板接口（要过权限协商，WebView2 里默认还要弹授权），
// 直接在 Go 侧调 Win32，稳且没有弹窗。
// 注意两点：剪贴板归属"打开它的线程"，所以 Open/Close 之间 LockOSThread；
// 别的程序占着剪贴板是常态，Open 要重试。

import (
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/win"
)

func clipboardOpen() bool {
	for i := 0; i < 10; i++ {
		if win.OpenClipboard(0) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// clipboardGet 读剪贴板文本；剪贴板里不是文本（图片等）时返回空串。
func clipboardGet() (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if !clipboardOpen() {
		return "", fmt.Errorf("剪贴板被别的程序占用")
	}
	defer win.CloseClipboard()

	h := win.GetClipboardData(win.CF_UNICODETEXT)
	if h == 0 {
		return "", nil
	}
	p := win.GlobalLock(win.HGLOBAL(h))
	if p == nil {
		return "", fmt.Errorf("锁定剪贴板内存失败")
	}
	defer win.GlobalUnlock(win.HGLOBAL(h))
	return win.UTF16PtrToString((*uint16)(p)), nil
}

// clipboardSet 把文本写进剪贴板（覆盖原内容）。
func clipboardSet(text string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if !clipboardOpen() {
		return fmt.Errorf("剪贴板被别的程序占用")
	}
	defer win.CloseClipboard()

	if !win.EmptyClipboard() {
		return fmt.Errorf("清空剪贴板失败")
	}
	utf16, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}
	size := uintptr(len(utf16) * 2)
	h := win.GlobalAlloc(win.GMEM_MOVEABLE, size)
	if h == 0 {
		return fmt.Errorf("分配剪贴板内存失败")
	}
	p := win.GlobalLock(h)
	if p == nil {
		win.GlobalFree(h)
		return fmt.Errorf("锁定剪贴板内存失败")
	}
	dst := unsafe.Slice((*uint16)(p), len(utf16))
	copy(dst, utf16)
	win.GlobalUnlock(h)
	if win.SetClipboardData(win.CF_UNICODETEXT, win.HANDLE(h)) == 0 {
		win.GlobalFree(h) // 失败才由我们释放；成功时内存归系统所有
		return fmt.Errorf("写入剪贴板失败")
	}
	return nil
}
