package main

// 入口：默认起无边框 WebView2 窗口（window.go）；-serve 退回本地网页模式（无桌面环境/测试用）。
// 两种模式共用同一套本地服务与页面；网页模式只绑 127.0.0.1。

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	serve := flag.Bool("serve", false, "网页模式：起本地服务并打开浏览器（缺省是原生窗口）")
	port := flag.Int("port", 0, "网页模式监听端口，缺省随机空闲端口")
	noBrowser := flag.Bool("no-browser", false, "网页模式下不自动打开浏览器")
	flag.Parse()

	if *serve {
		runServer(*port, *noBrowser)
		return
	}
	runGUI()
}

// startServer 起本地界面服务（只绑 127.0.0.1），返回页面地址。窗口模式与网页模式共用。
func startServer(port int) (string, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", err
	}
	server := &http.Server{Handler: newMux(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "服务异常：", err)
		}
	}()
	return fmt.Sprintf("http://%s/", listener.Addr().String()), nil
}

func runServer(port int, noBrowser bool) {
	url, err := startServer(port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "起服务失败：", err)
		os.Exit(1)
	}
	fmt.Println("aster-onboard // 中转站接入导入器（网页模式）")
	fmt.Println("界面地址：", url)
	fmt.Println("保持本窗口开启；用完直接关窗口或 Ctrl+C 退出。")
	if !noBrowser {
		openURL(url)
	}
	select {}
}
