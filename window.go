//go:build windows

package main

// 窗口层：无边框 Win32 顶层窗口 + WebView2 渲染（纯 Go 绑定，不用 cgo、不弹浏览器）。
//
// macOS 观感靠三层配合：
//   - 无边框（WS_POPUP + WS_THICKFRAME，客户区=整窗），圆角走 DWM（Win11）/区域裁剪（Win10）；
//   - 标题栏交给页面自绘（红黄绿灯），拖动与边缘缩放由 JS 发 HTTP 请求 → 主线程
//     ReleaseCapture + WM_NCLBUTTONDOWN(HT*)，之后由系统跑原生拖动/缩放回路，手感不打折；
//   - 页面配色跟系统主题（注册表 AppsUseLightTheme），窗口底色与页面底一致，加载不闪白。
//
// 缺 WebView2 运行时的机器（很少见）：退回默认浏览器打开同一套页面。

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"github.com/wailsapp/go-webview2/pkg/edge"
	"github.com/wailsapp/go-webview2/webviewloader"
	"golang.org/x/sys/windows/registry"
)

const (
	wmAppMinimize  = win.WM_APP + 1
	wmAppToggleMax = win.WM_APP + 2
	wmAppClose     = win.WM_APP + 3
	// wmAppDrag 的 wParam 是 HT* 码：HTCAPTION=整窗拖动，HTLEFT/HTTOPLEFT…=边缘缩放
	wmAppDrag = win.WM_APP + 4

	dwmwaWindowCornerPreference = 33 // DwmSetWindowAttribute 属性号（Win11 22000+）
	dwmcpRound                  = 2
	smCXAddedBorder             = 92 // SM_CXPADDEDBORDER，lxn/win 未收录
)

var (
	modDwmapi                 = syscall.NewLazyDLL("dwmapi.dll")
	procDwmSetWindowAttribute = modDwmapi.NewProc("DwmSetWindowAttribute")
	modGdi32                  = syscall.NewLazyDLL("gdi32.dll")
	procCreateSolidBrush      = modGdi32.NewProc("CreateSolidBrush")
	procCreateRoundRectRgn    = modGdi32.NewProc("CreateRoundRectRgn")
	modUser32                 = syscall.NewLazyDLL("user32.dll")
	procSetWindowRgn          = modUser32.NewProc("SetWindowRgn")
	modShell32                = syscall.NewLazyDLL("shell32.dll")
	procExtractIconEx         = modShell32.NewProc("ExtractIconExW")
)

// appIcons 从自己的 exe 里取出第 0 组图标（rsrc.syso 嵌的 app.ico）。
// 窗口类不设图标时，任务栏/Alt-Tab 只会给一个默认的"白纸"图标。
func appIcons() (big, small win.HICON) {
	exe, err := os.Executable()
	if err != nil {
		return 0, 0
	}
	p, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		return 0, 0
	}
	n, _, _ := procExtractIconEx.Call(uintptr(unsafe.Pointer(p)), 0,
		uintptr(unsafe.Pointer(&big)), uintptr(unsafe.Pointer(&small)), 1)
	if int32(n) <= 0 {
		return 0, 0
	}
	if small == 0 {
		small = big
	}
	return big, small
}

// ncCalcSizeParams 对应 Win32 NCCALCSIZE_PARAMS（只用到 rgrc[0]）。
type ncCalcSizeParams struct {
	Rgrc  [3]win.RECT
	LpPos uintptr
}

type winCtl struct {
	hwnd win.HWND
	web  *edge.Chromium
	dpi  uint32

	mu           sync.Mutex
	ready        bool // WebView2 控制器就绪
	roundOK      bool // DWM 圆角可用（Win11）；否则 WM_SIZE 时用窗口区域裁剪
	roundDecided bool // DWM 圆角已探测（此前不碰窗口区域）
	active       bool // 窗口是否前台（/api/win/mode 用）
	lastErr      error
}

// toggleMax 在最大化/还原之间切换。
func toggleMax(hwnd win.HWND) {
	if win.IsZoomed(hwnd) {
		win.ShowWindow(hwnd, win.SW_RESTORE)
	} else {
		win.ShowWindow(hwnd, win.SW_MAXIMIZE)
	}
}

var gWin *winCtl

func runGUI() {
	runtime.LockOSThread()

	url, err := startServer(0)
	if err != nil {
		messageBox("Aster 接入导入器", "本地界面服务起不来："+err.Error())
		return
	}

	// 缺运行时是最常见的环境问题，先探一下，别把用户丢在一个空白窗口前。
	if _, err := webviewloader.GetAvailableCoreWebView2BrowserVersionString(""); err != nil {
		if messageBoxOKCancel("缺少 WebView2 运行时",
			"本机没有找到 WebView2 运行时（Windows 11 或装过新版 Edge 的机器一般自带）。\n\n"+
				"点「确定」用默认浏览器打开配置页面；点「取消」退出。") == win.IDOK {
			openURL(url)
			select {}
		}
		return
	}

	gWin = &winCtl{dpi: 96}
	if !gWin.createWindow() {
		gWin = nil
		messageBox("Aster 接入导入器", "窗口创建失败。")
		return
	}

	// WebView2 初始化超时兜底（正常机器几毫秒就绪）。
	go func() {
		time.Sleep(25 * time.Second)
		gWin.mu.Lock()
		ready := gWin.ready
		gWin.mu.Unlock()
		if !ready {
			messageBox("Aster 接入导入器", "WebView2 初始化超时；请重装 WebView2 运行时后重试。")
			os.Exit(1)
		}
	}()

	web := edge.NewChromium()
	web.DataPath = filepath.Join(dataDir(), "webview2")
	web.SetErrorCallback(func(err error) {
		// 默认回调会直接 os.Exit；窗口模式下改成记录，尽量留在页面上把话说清楚。
		gWin.mu.Lock()
		gWin.lastErr = err
		gWin.mu.Unlock()
	})
	if !web.Embed(uintptr(gWin.hwnd)) {
		messageBox("Aster 接入导入器", "WebView2 初始化失败。")
		return
	}

	gWin.mu.Lock()
	gWin.web = web
	gWin.ready = true
	gWin.mu.Unlock()

	dark := appsUseDark()
	if dark {
		web.SetBackgroundColour(0x1e, 0x1e, 0x20, 0xff)
		web.Init("window.__asterTheme='dark';")
	} else {
		web.SetBackgroundColour(0xf2, 0xf2, 0xf5, 0xff)
		web.Init("window.__asterTheme='light';")
	}
	if st, err := web.GetSettings(); err == nil {
		_ = st.PutAreDefaultContextMenusEnabled(false)
		_ = st.PutIsZoomControlEnabled(false)
	}

	web.Resize()
	web.Navigate(url)
	web.Focus()

	var msg win.MSG
	for win.GetMessage(&msg, 0, 0, 0) > 0 {
		win.TranslateMessage(&msg)
		win.DispatchMessage(&msg)
	}
	web.ShuttingDown()
}

// monitorInfo 当前窗口所在显示器的信息（最大化要用它的工作区）。
func monitorInfo(hwnd win.HWND) *win.MONITORINFO {
	mi := win.MONITORINFO{CbSize: uint32(unsafe.Sizeof(win.MONITORINFO{}))}
	if !win.GetMonitorInfo(win.MonitorFromWindow(hwnd, win.MONITOR_DEFAULTTONEAREST), &mi) {
		return nil
	}
	return &mi
}

func (g *winCtl) createWindow() bool {
	hInst := win.GetModuleHandle(nil)
	className := syscall.StringToUTF16Ptr("AsterOnboardWnd")

	brush, _, _ := procCreateSolidBrush.Call(uintptr(windowBGR()))
	bigIcon, smallIcon := appIcons()
	wc := win.WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(win.WNDCLASSEX{})),
		Style:         win.CS_HREDRAW | win.CS_VREDRAW,
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     hInst,
		HIcon:         bigIcon,
		HIconSm:       smallIcon,
		HCursor:       win.LoadCursor(0, win.MAKEINTRESOURCE(uintptr(win.IDC_ARROW))),
		HbrBackground: win.HBRUSH(brush),
		LpszClassName: className,
	}
	win.RegisterClassEx(&wc) // 重复注册返回 0，无妨

	style := uint32(win.WS_POPUP | win.WS_THICKFRAME | win.WS_MINIMIZEBOX | win.WS_MAXIMIZEBOX |
		win.WS_SYSMENU | win.WS_CLIPCHILDREN)
	hwnd := win.CreateWindowEx(win.WS_EX_APPWINDOW, className,
		syscall.StringToUTF16Ptr("Aster 接入导入器"),
		style, 0, 0, 1080, 720, 0, 0, hInst, nil)
	if hwnd == 0 {
		return false
	}
	g.hwnd = hwnd
	if d := win.GetDpiForWindow(hwnd); d > 0 {
		g.dpi = d
	}

	wpx := int32(1080 * g.dpi / 96)
	hpx := int32(720 * g.dpi / 96)
	mi := win.MONITORINFO{CbSize: uint32(unsafe.Sizeof(win.MONITORINFO{}))}
	if win.GetMonitorInfo(win.MonitorFromWindow(hwnd, win.MONITOR_DEFAULTTONEAREST), &mi) {
		// 小屏笔记本上别超出工作区（各留 24 逻辑像素边距）。
		pad := int32(24 * g.dpi / 96)
		availW := mi.RcWork.Right - mi.RcWork.Left - pad
		availH := mi.RcWork.Bottom - mi.RcWork.Top - pad
		if wpx > availW {
			wpx = availW
		}
		if hpx > availH {
			hpx = availH
		}
		x := mi.RcWork.Left + (mi.RcWork.Right-mi.RcWork.Left-wpx)/2
		y := mi.RcWork.Top + (mi.RcWork.Bottom-mi.RcWork.Top-hpx)/2
		win.SetWindowPos(hwnd, win.HWND_TOP, x, y, wpx, hpx, win.SWP_NOACTIVATE)
	}
	win.ShowWindow(hwnd, win.SW_SHOW)
	win.UpdateWindow(hwnd)
	g.applyDwmRound()
	return true
}

// windowBGR 窗口底色（COLORREF，0x00BBGGRR），跟页面底一致。
func windowBGR() uint32 {
	if appsUseDark() {
		return 0x00201e1e // #1e1e20
	}
	return 0x00f5f2f2 // #f2f2f5
}

func appsUseDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.READ)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false
	}
	return v == 0
}

// applyDwmRound 试 Win11 的 DWM 圆角；不成（Win10）就留给 applyRoundRegion 裁剪。
func (g *winCtl) applyDwmRound() {
	pref := int32(dwmcpRound)
	r, _, _ := procDwmSetWindowAttribute.Call(uintptr(g.hwnd),
		uintptr(dwmwaWindowCornerPreference),
		uintptr(unsafe.Pointer(&pref)), unsafe.Sizeof(pref))
	ok := int32(r) == 0

	g.mu.Lock()
	g.roundDecided = true
	g.roundOK = ok
	g.mu.Unlock()

	if ok {
		// 关键：窗口创建期（WM_SIZE 早于本函数）可能已经走过区域裁剪那一路，
		// 那个区域会一直把窗口裁在"创建时的尺寸"上（最大化时只画左上角一块）。
		// DWM 自己能圆角时，把区域清掉。
		procSetWindowRgn.Call(uintptr(g.hwnd), 0, 1)
	}
}

// applyRoundRegion Win10 兜底：按窗口区域裁圆角（最大化时回直角）。
// roundDecided 之前（窗口创建早期）什么都不做——那时候设的区域会留在窗口上，
// 后续 roundOK=true 也不会清，把窗口裁死在旧尺寸。
func (g *winCtl) applyRoundRegion() {
	g.mu.Lock()
	ok, decided := g.roundOK, g.roundDecided
	g.mu.Unlock()
	if !decided || ok || g.hwnd == 0 {
		return
	}
	if win.IsZoomed(g.hwnd) {
		procSetWindowRgn.Call(uintptr(g.hwnd), 0, 1)
		return
	}
	var rc win.RECT
	if !win.GetWindowRect(g.hwnd, &rc) {
		return
	}
	d := int32(24 * g.dpi / 96) // CreateRoundRectRgn 收的是椭圆宽高，2 倍半径
	rgn, _, _ := procCreateRoundRectRgn.Call(0, 0,
		uintptr(rc.Right-rc.Left+1), uintptr(rc.Bottom-rc.Top+1), uintptr(d), uintptr(d))
	if rgn != 0 {
		procSetWindowRgn.Call(uintptr(g.hwnd), rgn, 1) // 成功后 region 归系统所有
	}
}

func (g *winCtl) resizeWeb() {
	g.mu.Lock()
	ready, web := g.ready, g.web
	g.mu.Unlock()
	if ready && web != nil {
		web.Resize()
	}
}

// setActive 记录窗口是否前台（页面不再消费，留给 /api/win/mode 与排障）。
func (g *winCtl) setActive(active bool) {
	g.mu.Lock()
	g.active = active
	g.mu.Unlock()
}

// isActive 当前是不是前台窗口（供 /api/win/mode 给页面初始状态）。
func (g *winCtl) isActive() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}

// perform 由 HTTP 处理函数调用（别的线程）：全部转成窗口消息，回主线程执行。
func (g *winCtl) perform(action, edgeName string) {
	switch action {
	case "minimize":
		win.PostMessage(g.hwnd, wmAppMinimize, 0, 0)
	case "maximize":
		win.PostMessage(g.hwnd, wmAppToggleMax, 0, 0)
	case "close":
		win.PostMessage(g.hwnd, wmAppClose, 0, 0)
	case "drag":
		win.PostMessage(g.hwnd, wmAppDrag, win.HTCAPTION, 0)
	case "resize":
		ht := map[string]uintptr{
			"top": win.HTTOP, "bottom": win.HTBOTTOM, "left": win.HTLEFT, "right": win.HTRIGHT,
			"topleft": win.HTTOPLEFT, "topright": win.HTTOPRIGHT,
			"bottomleft": win.HTBOTTOMLEFT, "bottomright": win.HTBOTTOMRIGHT,
		}[edgeName]
		if ht != 0 {
			win.PostMessage(g.hwnd, wmAppDrag, ht, 0)
		}
	}
}

func wndProc(hwnd win.HWND, msg uint32, wparam, lparam uintptr) uintptr {
	g := gWin
	switch msg {
	case win.WM_NCCALCSIZE:
		if wparam == 0 {
			break
		}
		// 无边框：客户区=整窗；最大化时按窗口边框内缩，免得内容顶出屏幕。
		// 注：lParam 是系统写入的合法指针，(*T)(unsafe.Pointer(lParam)) 是读消息参数的唯一办法；
		// go vet 会在这类转换上报 "possible misuse of unsafe.Pointer"，属预期噪声。
		if win.IsZoomed(hwnd) {
			params := (*ncCalcSizeParams)(unsafe.Pointer(lparam))
			r := &params.Rgrc[0]
			cx := win.GetSystemMetrics(win.SM_CXSIZEFRAME) + win.GetSystemMetrics(smCXAddedBorder)
			cy := win.GetSystemMetrics(win.SM_CYSIZEFRAME) + win.GetSystemMetrics(smCXAddedBorder)
			r.Left += cx
			r.Right -= cx
			r.Top += cy
			r.Bottom -= cy
		}
		return 0

	case win.WM_GETMINMAXINFO:
		if g != nil {
			mmi := (*win.MINMAXINFO)(unsafe.Pointer(lparam))
			mmi.PtMinTrackSize = win.POINT{X: int32(880 * g.dpi / 96), Y: int32(540 * g.dpi / 96)}
			// 自绘无边框窗口必须自己把"最大化"算对：系统默认按主屏 (0,0) 算，
			// 多显示器时窗口会跳到别的屏左上角，客户区还会被边框顶出屏幕。
			// 配方同 Chromium/Electron：尺寸=工作区+两侧边框，位置=工作区左上再挪一份边框；
			// 配合 WM_NCCALCSIZE 里的同量内缩，客户区正好等于工作区。
			if mi := monitorInfo(hwnd); mi != nil {
				cx := win.GetSystemMetrics(win.SM_CXSIZEFRAME) + win.GetSystemMetrics(smCXAddedBorder)
				cy := win.GetSystemMetrics(win.SM_CYSIZEFRAME) + win.GetSystemMetrics(smCXAddedBorder)
				// PtMaxPosition 是相对该显示器左上角的（实测：副屏上按屏幕坐标给会把窗口顶到右边缘）。
				mmi.PtMaxPosition = win.POINT{
					X: mi.RcWork.Left - mi.RcMonitor.Left - cx,
					Y: mi.RcWork.Top - mi.RcMonitor.Top - cy,
				}
				mmi.PtMaxSize = win.POINT{
					X: (mi.RcWork.Right - mi.RcWork.Left) + cx*2,
					Y: (mi.RcWork.Bottom - mi.RcWork.Top) + cy*2,
				}
			}
			return 0
		}

	case win.WM_ACTIVATE:
		// 只记录前台状态（/api/win/mode 会报），不驱动红黄绿灯——
		// 三个点恒亮，不做"失焦变灰"。
		if g != nil {
			g.setActive(wparam&0xFFFF != 0) // WA_INACTIVE=0
		}

	case win.WM_SIZE:
		if g != nil {
			g.resizeWeb()
			g.applyRoundRegion()
		}
		return 0

	case win.WM_DPICHANGED:
		if g != nil {
			g.dpi = uint32(win.LOWORD(uint32(wparam)))
			suggested := (*win.RECT)(unsafe.Pointer(lparam))
			win.SetWindowPos(hwnd, win.HWND_TOP, suggested.Left, suggested.Top,
				suggested.Right-suggested.Left, suggested.Bottom-suggested.Top,
				win.SWP_NOZORDER|win.SWP_NOACTIVATE)
			g.applyRoundRegion()
		}
		return 0

	case win.WM_ERASEBKGND:
		return 1

	case wmAppMinimize:
		win.ShowWindow(hwnd, win.SW_MINIMIZE)
		if !win.IsIconic(hwnd) {
			// 个别状态下 SW_MINIMIZE 会被系统忽略，退回系统菜单路径。
			win.SendMessage(hwnd, win.WM_SYSCOMMAND, win.SC_MINIMIZE, 0)
		}
		return 0

	case wmAppToggleMax:
		toggleMax(hwnd)
		if g != nil {
			g.applyRoundRegion()
		}
		return 0

	case wmAppClose:
		win.DestroyWindow(hwnd)
		return 0

	case wmAppDrag:
		// 页面按下标题栏/边缘 → 这里把拖动/缩放交回系统的原生回路。
		win.ReleaseCapture()
		var pt win.POINT
		if win.GetCursorPos(&pt) {
			lp := uintptr(uint16(pt.X)) | uintptr(uint16(pt.Y))<<16
			win.SendMessage(hwnd, win.WM_NCLBUTTONDOWN, wparam, lp)
		}
		return 0

	case win.WM_SYSCOMMAND:
		// 吃掉 Alt 触发的系统菜单（无边框窗口上会弹在奇怪的位置）。
		if wparam&0xFFF0 == win.SC_KEYMENU {
			return 0
		}

	case win.WM_CLOSE:
		win.DestroyWindow(hwnd)
		return 0

	case win.WM_DESTROY:
		win.PostQuitMessage(0)
		return 0
	}
	return win.DefWindowProc(hwnd, msg, wparam, lparam)
}

func messageBox(title, text string) {
	win.MessageBox(0, syscall.StringToUTF16Ptr(text), syscall.StringToUTF16Ptr(title),
		win.MB_OK|win.MB_ICONINFORMATION)
}

func messageBoxOKCancel(title, text string) int32 {
	return win.MessageBox(0, syscall.StringToUTF16Ptr(text), syscall.StringToUTF16Ptr(title),
		win.MB_OKCANCEL|win.MB_ICONINFORMATION)
}
