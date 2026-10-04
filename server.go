package main

// 本地服务：只绑 127.0.0.1，页面与四个接口。密钥只存在于内存与写出去的客户端配置里，不落日志。

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

//go:embed ui.html
var uiHTML string

type scanRow struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Kind      string        `json:"kind"`
	Installed bool          `json:"installed"`
	Evidence  []string      `json:"evidence"`
	Managed   bool          `json:"managed"`
	Paths     []string      `json:"paths"`
	BackupAt  string        `json:"backupAt"`
	WrittenAt string        `json:"writtenAt"` // 最近一次成功写入（「恢复」用）
	Applied   *appliedState `json:"applied,omitempty"`
}

func handleScan(w http.ResponseWriter, _ *http.Request) {
	state := loadState()
	var rows []scanRow
	for _, c := range allClients() {
		evidence := c.Detect(homeDir())
		row := scanRow{
			ID:        c.ID,
			Name:      c.Name,
			Kind:      c.Kind,
			Installed: len(evidence) > 0,
			Evidence:  evidence,
			Paths:     c.Paths(homeDir()),
		}
		if row.Installed {
			row.Managed = c.Managed(homeDir())
		}
		if b := latestBackup(c.ID); b != nil {
			row.BackupAt = b.CreatedAt
		}
		if w := latestWrittenBackup(c.ID); w != nil {
			row.WrittenAt = w.CreatedAt
		}
		if st, ok := state[c.ID]; ok {
			row.Applied = &st
		}
		rows = append(rows, row)
	}
	writeJSON(w, rows)
}

// catalogItem 是中转站 /v1/models 的一条：id 必带，credits 是站点下发的消耗倍率
// （形如 "x0.80" / "≈58.1 积分/秒"）。倍率原样透传给客户端，不在这里做换算——
// 站点口径是「实际服务那家的倍率」，客户端只负责把它显示出来。
type catalogItem struct {
	ID      string `json:"id"`
	Credits string `json:"credits,omitempty"`
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(r.URL.Query().Get("base"), "/")
	key := r.URL.Query().Get("key")
	if base == "" || key == "" {
		http.Error(w, "缺 base 或 key", http.StatusBadRequest)
		return
	}
	models, err := fetchModels(base, key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	writeJSON(w, map[string]any{"models": ids, "catalog": models})
}

func fetchModels(base, key string) ([]catalogItem, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上中转站：%w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("中转站返回 %d：%s", resp.StatusCode, firstLine(string(body)))
	}
	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			Credits string `json:"credits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("模型清单解析失败：%w", err)
	}
	var out []catalogItem
	for _, d := range payload.Data {
		if d.ID != "" {
			out = append(out, catalogItem{ID: d.ID, Credits: strings.TrimSpace(d.Credits)})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("中转站模型清单为空")
	}
	return out, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

type applyRequest struct {
	Base   string   `json:"base"`
	Key    string   `json:"key"`
	Models []string `json:"models"`
	// Rates 是 模型 id → 中转站下发的倍率文案（/v1/models 的 credits），
	// 原样写进客户端配置，不在本工具里换算。
	Rates map[string]string `json:"rates"`
	Items []struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	} `json:"items"`
}

func handleApply(w http.ResponseWriter, r *http.Request) {
	var req applyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求体不是合法 JSON", http.StatusBadRequest)
		return
	}
	req.Base = strings.TrimRight(strings.TrimSpace(req.Base), "/")
	if req.Base == "" || req.Key == "" || len(req.Items) == 0 {
		http.Error(w, "缺地址、密钥或客户端", http.StatusBadRequest)
		return
	}
	clients := map[string]Client{}
	for _, c := range allClients() {
		clients[c.ID] = c
	}
	results := map[string]any{}
	for _, item := range req.Items {
		c, ok := clients[item.ID]
		if !ok {
			results[item.ID] = map[string]any{"ok": false, "error": "未知客户端"}
			continue
		}
		backupID, err := applyClient(c, Params{Base: req.Base, Key: req.Key, Model: item.Model, Models: req.Models, Rates: req.Rates})
		if err != nil {
			results[item.ID] = map[string]any{"ok": false, "error": err.Error()}
			continue
		}
		results[item.ID] = map[string]any{"ok": true, "backup": backupID}
	}
	writeJSON(w, results)
}

func handleRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Mode string `json:"mode"` // "after"（默认）= 恢复成工具写好的配置；"before" = 撤销写入
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求体不是合法 JSON", http.StatusBadRequest)
		return
	}
	after := req.Mode != "before"
	var b *backupMeta
	if after {
		b = latestWrittenBackup(req.ID)
	} else {
		b = latestBackup(req.ID)
	}
	if b == nil {
		if after {
			http.Error(w, "该客户端还没有写入记录", http.StatusNotFound)
		} else {
			http.Error(w, "该客户端没有备份", http.StatusNotFound)
		}
		return
	}
	if err := restoreBackup(req.ID, b.ID, after); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "restored": b.ID, "mode": map[bool]string{true: "after", false: "before"}[after]})
}

func handleBackups(w http.ResponseWriter, r *http.Request) {
	client := r.URL.Query().Get("client")
	writeJSON(w, listBackups(client))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, uiHTML)
	})
	mux.HandleFunc("/api/scan", handleScan)
	mux.HandleFunc("/api/models", handleModels)
	mux.HandleFunc("/api/apply", handleApply)
	mux.HandleFunc("/api/restore", handleRestore)
	mux.HandleFunc("/api/backups", handleBackups)
	mux.HandleFunc("/api/win/mode", handleWinMode)
	mux.HandleFunc("/api/win/action", handleWinAction)
	mux.HandleFunc("/api/clipboard", handleClipboard)
	mux.HandleFunc("/api/settings", handleSettings)
	mux.HandleFunc("/api/ping", handlePing)
	mux.HandleFunc("/api/bye", handleBye)
	return mux
}

// ---------- 页面心跳（Mac 的浏览器窗口模式：关掉界面就让本程序退出） ----------

var (
	lastPing atomic.Int64
	lastBye  atomic.Int64
)

func handlePing(w http.ResponseWriter, _ *http.Request) {
	lastPing.Store(time.Now().Unix())
	w.WriteHeader(http.StatusNoContent)
}

func handleBye(w http.ResponseWriter, _ *http.Request) {
	lastBye.Store(time.Now().Unix())
	w.WriteHeader(http.StatusNoContent)
}

// waitForPageGone 等界面彻底消失：心跳停 >90 秒、或收到送别后 20 秒仍无新心跳就返回；
// 页面从来没起来过（比如浏览器没打开）则 5 分钟后返回，免得留一个看不见的常驻进程。
func waitForPageGone() {
	started := time.Now()
	for {
		time.Sleep(3 * time.Second)
		now := time.Now()
		if t := lastPing.Load(); t != 0 {
			if now.Unix()-t > 90 {
				return
			}
			continue
		}
		if b := lastBye.Load(); b != 0 && now.Unix()-b > 20 {
			return
		}
		if now.Sub(started) > 5*time.Minute {
			return
		}
	}
}

// ---------- 本机设置（记住上次用的密钥与接口地址，免得每次重输） ----------
// 只落在本机数据目录（%LOCALAPPDATA%\aster-onboard\settings.json），不下发、不进日志。

type localSettings struct {
	Base string `json:"base"`
	Key  string `json:"key"`
}

func settingsPath() string { return filepath.Join(dataDir(), "settings.json") }

func loadSettings() localSettings {
	raw, err := os.ReadFile(settingsPath())
	if err != nil {
		return localSettings{}
	}
	var v localSettings
	if json.Unmarshal(raw, &v) != nil {
		return localSettings{}
	}
	return v
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, loadSettings())
	case http.MethodPost:
		var req localSettings
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "请求体不是合法 JSON", http.StatusBadRequest)
			return
		}
		req.Base = strings.TrimRight(strings.TrimSpace(req.Base), "/")
		req.Key = strings.TrimSpace(req.Key)
		if req.Key == "" {
			writeJSON(w, map[string]any{"ok": true, "saved": false}) // 空密钥不覆盖已存的
			return
		}
		if err := os.MkdirAll(dataDir(), 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		raw, err := json.MarshalIndent(req, "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(settingsPath(), raw, 0o600); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "saved": true})
	default:
		http.Error(w, "只支持 GET/POST", http.StatusMethodNotAllowed)
	}
}

// ---------- 剪贴板（页面自绘右键菜单的复制/粘贴；Go 直读系统剪贴板） ----------

func handleClipboard(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		text, err := clipboardGet()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"text": text})
	case http.MethodPost:
		var req struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "请求体不是合法 JSON", http.StatusBadRequest)
			return
		}
		if err := clipboardSet(req.Text); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "只支持 GET/POST", http.StatusMethodNotAllowed)
	}
}

// ---------- 窗口控制（只有窗口模式可用；网页模式页面会隐藏红黄绿灯） ----------

func handleWinMode(w http.ResponseWriter, _ *http.Request) {
	active := false
	if gWin != nil {
		active = gWin.isActive()
	}
	writeJSON(w, map[string]any{
		"window": gWin != nil,
		"dark":   appsUseDark(),
		"active": active,
	})
}

func handleWinAction(w http.ResponseWriter, r *http.Request) {
	if gWin == nil {
		http.Error(w, "网页模式没有窗口可操作", http.StatusForbidden)
		return
	}
	var req struct {
		Action string `json:"action"`
		Edge   string `json:"edge"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求体不是合法 JSON", http.StatusBadRequest)
		return
	}
	switch req.Action {
	case "minimize", "maximize", "close", "drag":
	case "resize":
		switch req.Edge {
		case "top", "bottom", "left", "right", "topleft", "topright", "bottomleft", "bottomright":
		default:
			http.Error(w, "未知缩放边", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "未知窗口动作", http.StatusBadRequest)
		return
	}
	gWin.perform(req.Action, req.Edge)
	writeJSON(w, map[string]any{"ok": true})
}
