package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ASTER_ONBOARD_HOME", home)
	t.Setenv("ASTER_ONBOARD_DATA", filepath.Join(home, ".data"))
	return home
}

func clientByID(t *testing.T, id string) Client {
	t.Helper()
	for _, c := range allClients() {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("未知客户端 %s", id)
	return Client{}
}

func params() Params {
	return Params{Base: "https://relay.example.top", Key: "sk-test", Model: "glm-5", Models: []string{"glm-5", "qwen3-max"}}
}

func mustApply(t *testing.T, id string) {
	t.Helper()
	if _, err := applyClient(clientByID(t, id), params()); err != nil {
		t.Fatalf("%s 写入失败：%v", id, err)
	}
}

func TestClaudeCodeMergesAndMarks(t *testing.T) {
	home := sandbox(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settings), 0o755)
	os.WriteFile(settings, []byte(`{"env":{"FOO":"bar"},"theme":"dark"}`), 0o644)

	mustApply(t, "claude-code")
	root := readJSON(settings)
	env := root["env"].(map[string]any)
	if env["FOO"] != "bar" || root["theme"] != "dark" {
		t.Fatal("无关键被弄丢")
	}
	if env["ANTHROPIC_BASE_URL"] != "https://relay.example.top" || env["ANTHROPIC_AUTH_TOKEN"] != "sk-test" {
		t.Fatal("Anthropic 环境变量不对")
	}
	if env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] != "1" {
		t.Fatal("没开网关模型发现，Claude Code 拿不到全量清单")
	}
	if root["model"] != "glm-5" {
		t.Fatal("默认模型没写")
	}
	if !clientByID(t, "claude-code").Managed(home) {
		t.Fatal("托管标记没认出来")
	}
}

func TestClaudeDesktopListsAllModels(t *testing.T) {
	sandbox(t)
	mustApply(t, "claude-desktop")
	paths := claudeDesktopPaths(homeDir())
	profile := readJSON(paths[2])
	models := profile["inferenceModels"].([]any)
	if len(models) != 2 {
		t.Fatalf("推理模型应囊括全量清单，实际 %d", len(models))
	}
	if models[0].(map[string]any)["name"] != "glm-5" || models[1].(map[string]any)["name"] != "qwen3-max" {
		t.Fatal("推理模型名单不对")
	}
	meta := readJSON(paths[3])
	if meta["appliedId"] != claudeDesktopProfileID {
		t.Fatal("appliedId 不对")
	}
}

func TestCodexPreservesCommentsAndSections(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, ".codex")
	os.MkdirAll(dir, 0o755)
	original := "# 我的手写注释\nmodel = \"gpt-6\"\n\n[profiles.dev]\napproval_policy = \"never\"\n"
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(original), 0o644)

	mustApply(t, "codex")
	src := readText(filepath.Join(dir, "config.toml"))
	for _, want := range []string{"# 我的手写注释", "[profiles.dev]", `model_provider = "aster"`, `wire_api = "responses"`, `model_catalog_json = "aster-model-catalog.json"`} {
		if !strings.Contains(src, want) {
			t.Fatalf("config.toml 缺 %q：\n%s", want, src)
		}
	}
	if tomlTopValue(src, "model") != `"glm-5"` {
		t.Fatal("顶层 model 没被替换")
	}
	auth := readJSON(filepath.Join(dir, "auth.json"))
	if auth["auth_mode"] != "apikey" || auth["OPENAI_API_KEY"] != "sk-test" {
		t.Fatal("auth.json 不对")
	}
	catalog := readJSON(filepath.Join(dir, catalogFile))
	models := catalog["models"].([]any)
	if len(models) != 2 || models[0].(map[string]any)["slug"] != "glm-5" {
		t.Fatal("catalog 不对")
	}
	if !clientByID(t, "codex").Managed(home) {
		t.Fatal("codex 托管标记没认出来")
	}
}

func TestZCodeIdempotent(t *testing.T) {
	home := sandbox(t)
	mustApply(t, "zcode")
	mustApply(t, "zcode")
	root := readJSON(filepath.Join(home, ".zcode", "v2", "provider_config.json"))
	rules := root["config"].(map[string]any)["providerConfigRules"].(map[string]any)["providerRules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("写两遍应只留一条托管规则，实际 %d", len(rules))
	}
	sel := root["config"].(map[string]any)["defaultModelSelection"].(map[string]any)
	if sel["providerId"] != "aster" || sel["modelId"] != "glm-5" {
		t.Fatal("默认选择不对")
	}
	if !clientByID(t, "zcode").Managed(home) {
		t.Fatal("zcode 托管标记没认出来")
	}
}

func TestWorkBuddyArrayAndObjectForms(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, ".workbuddy-ai")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "models.json")

	os.WriteFile(path, []byte(`[{"id":"mine","vendor":"x"}]`), 0o644)
	mustApply(t, "workbuddy")
	var arr []map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatal(err)
	}
	if len(arr) != 3 || arr[1]["vendor"] != "aster" || arr[2]["id"] != "qwen3-max" ||
		!strings.HasSuffix(arr[1]["url"].(string), "/v1/chat/completions") {
		t.Fatalf("数组形态应保留自定义+全量托管条目：%s", raw)
	}

	os.WriteFile(path, []byte(`{"models":[{"id":"mine","vendor":"x"}],"availableModels":["mine"]}`), 0o644)
	mustApply(t, "workbuddy")
	obj := readJSON(path)
	models := obj["models"].([]any)
	visible := obj["availableModels"].([]any)
	if len(models) != 3 || len(visible) != 3 || visible[2] != "qwen3-max" {
		t.Fatalf("对象形态应保留自定义+全量托管条目：%s", readText(path))
	}
	if !clientByID(t, "workbuddy").Managed(home) {
		t.Fatal("workbuddy 托管标记没认出来")
	}
}

func TestWorkBuddyWritesCreditsRate(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, ".workbuddy-ai")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "models.json")

	p := params()
	p.Rates = map[string]string{"glm-5": "x0.80", "qwen3-max": "≈58.1 积分/秒"}
	if _, err := applyClient(clientByID(t, "workbuddy"), p); err != nil {
		t.Fatal(err)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(readText(path)), &arr); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, m := range arr {
		byID[m["id"].(string)] = m
	}
	if byID["glm-5"]["credits"] != "x0.80" {
		t.Fatalf("glm-5 应带上倍率 x0.80，实际 %v", byID["glm-5"]["credits"])
	}
	// 非 x 开头的文案（按秒计费的媒体模型）也要原样带过去，客户端自己显示
	if byID["qwen3-max"]["credits"] != "≈58.1 积分/秒" {
		t.Fatalf("qwen3-max 应带上倍率文案，实际 %v", byID["qwen3-max"]["credits"])
	}

	// 站点这次没下发该模型的倍率 ⇒ 不能留着上一轮的旧值
	p.Rates = map[string]string{"glm-5": "x0.80"}
	if _, err := applyClient(clientByID(t, "workbuddy"), p); err != nil {
		t.Fatal(err)
	}
	arr = nil
	if err := json.Unmarshal([]byte(readText(path)), &arr); err != nil {
		t.Fatal(err)
	}
	for _, m := range arr {
		if m["id"] == "qwen3-max" {
			if _, ok := m["credits"]; ok {
				t.Fatalf("没有倍率的模型不该留 credits 字段：%v", m)
			}
		}
	}
}

// TestFetchModelsCarriesCredits：中转站 /v1/models 的 credits 必须一路带回前端，
// 丢了它 WorkBuddy 就显示不出倍率（曾经只取 id 的 bug）。
func TestFetchModelsCarriesCredits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("路径不对：%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Fatalf("没带密钥：%q", r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"data":[{"id":"glm-5","credits":"x0.80"},{"id":"media","credits":"≈58.1 积分/秒"},{"id":"free","credits":""},{"id":"","credits":"x1"}]}`)
	}))
	defer srv.Close()

	items, err := fetchModels(srv.URL, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("空 id 的条目应被丢掉，实际 %d 条：%+v", len(items), items)
	}
	if items[0].ID != "glm-5" || items[0].Credits != "x0.80" {
		t.Fatalf("credits 没带回来：%+v", items[0])
	}
	if items[1].Credits != "≈58.1 积分/秒" {
		t.Fatalf("非 x 开头的倍率文案应原样保留：%+v", items[1])
	}
	if items[2].Credits != "" {
		t.Fatalf("空 credits 应是空串而不是缺字段：%+v", items[2])
	}
}

func TestOpenCodeKeepsComments(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, ".config", "opencode")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "opencode.json")
	os.WriteFile(path, []byte("// 我的注释\n{\"model\":\"old\"}\n"), 0o644)

	mustApply(t, "opencode")
	out := readText(path)
	if !strings.HasPrefix(out, "// 我的注释\n") {
		t.Fatalf("注释没保留：\n%s", out)
	}
	root := readJSON(path)
	provider := root["provider"].(map[string]any)["aster"].(map[string]any)
	if provider["npm"] != "@ai-sdk/openai-compatible" {
		t.Fatal("npm 包名不对")
	}
	if root["model"] != "aster/glm-5" {
		t.Fatal("默认模型不对")
	}
}

func TestHermesYaml(t *testing.T) {
	home := sandbox(t)
	path := hermesConfigPath(home)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("# 注释\ncustom_providers:\n  - name: other\n    base_url: http://x\n"), 0o644)

	mustApply(t, "hermes")
	out := readText(path)
	if !strings.Contains(out, "# 注释") || !strings.Contains(out, "name: aster") || !strings.Contains(out, "provider: aster") {
		t.Fatalf("hermes yaml 不对：\n%s", out)
	}
	if !strings.Contains(out, "name: other") {
		t.Fatal("别人的 provider 被弄丢")
	}
}

func TestKimiAndGrokSections(t *testing.T) {
	home := sandbox(t)
	for _, id := range []string{"kimi", "grok"} {
		mustApply(t, id)
	}
	kimi := readText(filepath.Join(home, ".kimi-code", "config.toml"))
	if !strings.Contains(kimi, `[providers.aster]`) || !strings.Contains(kimi, `[models."aster/glm-5"]`) || !strings.Contains(kimi, `[models."aster/qwen3-max"]`) {
		t.Fatalf("kimi toml 不对：\n%s", kimi)
	}
	grok := readText(filepath.Join(home, ".grok", "config.toml"))
	if !strings.Contains(grok, `[model."aster/glm-5"]`) || !strings.Contains(grok, "api_backend = \"chat_completions\"") {
		t.Fatalf("grok toml 不对：\n%s", grok)
	}
	// 模型目录缩水后重写，过期段要清掉
	p := params()
	p.Models = []string{"glm-5"}
	if _, err := applyClient(clientByID(t, "kimi"), p); err != nil {
		t.Fatal(err)
	}
	kimi = readText(filepath.Join(home, ".kimi-code", "config.toml"))
	if strings.Contains(kimi, `aster/qwen3-max`) {
		t.Fatalf("过期模型段没清：\n%s", kimi)
	}
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	home := sandbox(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settings), 0o755)
	original := `{"env":{"FOO":"bar"}}`
	os.WriteFile(settings, []byte(original), 0o644)

	mustApply(t, "claude-code")
	if readText(settings) == original {
		t.Fatal("配置没被改写")
	}
	b := latestBackup("claude-code")
	if b == nil {
		t.Fatal("没有备份")
	}
	if err := restoreBackup("claude-code", b.ID, false); err != nil {
		t.Fatal(err)
	}
	if readText(settings) != original {
		t.Fatalf("恢复后不是原文：%s", readText(settings))
	}
	if clientByID(t, "claude-code").Managed(home) {
		t.Fatal("恢复后不应再算托管")
	}

	// 「恢复」= 还原成本工具写入的样子（写入后快照）
	mustApply(t, "claude-code")
	written := readText(settings)
	if written == original {
		t.Fatal("写入没有生效")
	}
	os.Remove(settings) // 用户把文件删了
	w := latestWrittenBackup("claude-code")
	if w == nil {
		t.Fatal("没有写入后快照")
	}
	if err := restoreBackup("claude-code", w.ID, true); err != nil {
		t.Fatal(err)
	}
	if readText(settings) != written {
		t.Fatalf("恢复成写入结果失败：%s", readText(settings))
	}

	// 原本不存在的文件：写入后再恢复应被删掉
	os.Remove(settings)
	mustApply(t, "claude-code")
	if _, err := os.Stat(settings); err != nil {
		t.Fatal("写入应创建文件")
	}
	b = latestBackup("claude-code")
	restoreBackup("claude-code", b.ID, false)
	if _, err := os.Stat(settings); err == nil {
		t.Fatal("原本不存在的文件恢复后应删除")
	}
}

func TestHTTPScanApplyRestore(t *testing.T) {
	sandbox(t)
	srv := httptest.NewServer(newMux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/scan")
	if err != nil {
		t.Fatal(err)
	}
	var rows []scanRow
	json.NewDecoder(resp.Body).Decode(&rows)
	if len(rows) != len(allClients()) {
		t.Fatalf("扫描行数不对：%d", len(rows))
	}

	body := `{"base":"https://relay.example.top","key":"sk-test","models":["glm-5"],"items":[{"id":"zcode","model":"glm-5"}]}`
	resp, err = http.Post(srv.URL+"/api/apply", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]struct {
		Ok    bool   `json:"ok"`
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if !out["zcode"].Ok {
		t.Fatalf("apply 失败：%s", out["zcode"].Error)
	}

	resp, err = http.Post(srv.URL+"/api/restore", "application/json", strings.NewReader(`{"id":"zcode"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restore 状态码 %d", resp.StatusCode)
	}
}
