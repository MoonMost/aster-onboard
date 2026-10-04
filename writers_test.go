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
	return Params{
		Base:   "https://relay.example.top",
		Key:    "sk-test",
		Model:  "glm-5",
		Models: []string{"glm-5", "qwen3-max"},
		// 能力元数据与站点 /v1/models 同形：glm-5 可关思考、qwen3-max 仅思考，媒体与规格各报各的。
		Catalog: []catalogItem{
			{
				ID: "glm-5", SupportsReasoning: bp(true), OnlyReasoning: bp(false),
				ReasoningEfforts: []string{"low", "medium", "high"}, DefaultEffort: "high",
				SupportsImages: bp(true), SupportsVideo: bp(false), SupportsToolCall: bp(true),
				ContextLength: i64(1000000), MaxAllowedSize: i64(168000), MaxOutputTokens: i64(128000),
				Type: "chat",
			},
			{
				ID: "qwen3-max", SupportsReasoning: bp(true), OnlyReasoning: bp(true),
				ReasoningEfforts: []string{"high"}, DefaultEffort: "high",
				SupportsImages: bp(false), SupportsVideo: bp(false), SupportsToolCall: bp(true),
				ContextLength: i64(400000), MaxAllowedSize: i64(400000), MaxOutputTokens: i64(32000),
				Type: "chat",
			},
		},
	}
}

func bp(v bool) *bool    { return &v }
func i64(v int64) *int64 { return &v }

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
	// 必须是 OpenAI 协议 + /v1 根：档位只有在这条路上才会以 reasoning_effort 发出去
	// （anthropic-messages 路的 output_config.effort 会被网关丢掉，档位就成了摆设）。
	cfg := rules[0].(map[string]any)["config"].(map[string]any)
	api := cfg["api"].(map[string]any)
	if api["type"] != "openai-chat-completions" || api["baseUrl"] != "https://relay.example.top/v1" {
		t.Fatalf("ZCode 应走 OpenAI 协议 + /v1：%v", api)
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

// TestWorkBuddyWritesCreditsRate：倍率那一格的取值**必须与官网模型清单同值**——
// 聊天行 = landing_rate + " 积分/千token"（客户端 formatCredits 只取第一个数字画成 "Nx"），
// 媒体行 = credits 原文（"≈N 积分/秒"）。与门户「完整配置导出」同一口径（clientRateHint）。
func TestWorkBuddyWritesCreditsRate(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, ".workbuddy-ai")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "models.json")

	p := params()
	p.Catalog[0].LandingRate = "0.80" // glm-5：官网上那一格
	p.Catalog[1].Credits = "≈58.1 积分/秒"
	p.Catalog[1].Type = "video" // qwen3-max 当作媒体行
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
	if byID["glm-5"]["credits"] != "0.80 积分/千token" {
		t.Fatalf("聊天行应写官网口径的倍率提示，实际 %v", byID["glm-5"]["credits"])
	}
	// 媒体行：按秒计费的文案原样带过去，客户端自己显示
	if byID["qwen3-max"]["credits"] != "≈58.1 积分/秒" {
		t.Fatalf("媒体行应带上按秒的倍率文案，实际 %v", byID["qwen3-max"]["credits"])
	}

	// 站点这次没下发该模型的倍率 ⇒ 不能留着上一轮的旧值
	p.Catalog[1].Credits = ""
	p.Catalog[1].Type = ""
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

// TestZCodeWritesModelCapabilities：档位下拉只读 modelConfigRules.providerModelRules 里的
// optionSpecs.reasoningLevel.values；不写这一层，ZCode 里这些自定义模型名撞不上内置正则，
// 思考档位就只剩「开启/关闭」两态（Space-Bunny 五档选不了的根因）。上下文与输入类型同理。
func TestZCodeWritesModelCapabilities(t *testing.T) {
	home := sandbox(t)
	path := filepath.Join(home, ".zcode", "v2", "provider_config.json")
	// 预置：另一个 provider 的规则 + 一条 aster 规则（带手设属性与 enabled:false）
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"schemaVersion":1,"config":{"modelConfigRules":{"providerModelRules":[
		{"providerId":"custom:other","modelId":"x","config":{"enabled":false}},
		{"providerId":"aster","modelId":"glm-5","config":{"enabled":false,"properties":{"supportsNativeWebSearch":true}}}
	]}}}`), 0o644)

	mustApply(t, "zcode")
	mustApply(t, "zcode") // 幂等：再写一遍不能多出规则

	root := readJSON(path)
	rules := root["config"].(map[string]any)["modelConfigRules"].(map[string]any)["providerModelRules"].([]any)
	if len(rules) != 3 {
		t.Fatalf("应有 3 条规则（别人的 + glm-5 + qwen3-max），实际 %d", len(rules))
	}
	byID := map[string]map[string]any{}
	for _, r := range rules {
		m := r.(map[string]any)
		if m["providerId"] == "aster" {
			byID[m["modelId"].(string)] = m
		}
	}
	glm := byID["glm-5"]
	if glm == nil {
		t.Fatal("glm-5 规则丢了")
	}
	cfg := glm["config"].(map[string]any)
	if cfg["enabled"] != false {
		t.Fatal("既有规则的 enabled 被改动了")
	}
	props := cfg["properties"].(map[string]any)
	if props["supportsNativeWebSearch"] != true {
		t.Fatal("既有规则的手设属性被弄丢")
	}
	if props["contextWindow"] != float64(1000000) {
		t.Fatalf("上下文窗口没写进覆盖层：%v", props["contextWindow"])
	}
	fmtIn := props["inputFormat"].(map[string]any)
	if fmtIn["supportsImage"] != true || fmtIn["supportsVideo"] != false {
		t.Fatalf("输入类型不对：%v", fmtIn)
	}
	levels := cfg["optionSpecs"].(map[string]any)["reasoningLevel"].(map[string]any)["values"].([]any)
	if len(levels) != 3 || levels[0] != "low" || levels[2] != "high" {
		t.Fatalf("档位清单不对：%v", levels)
	}
	qwen := byID["qwen3-max"]
	if qwen == nil {
		t.Fatal("qwen3-max 规则没建立")
	}
	qLevels := qwen["config"].(map[string]any)["optionSpecs"].(map[string]any)["reasoningLevel"].(map[string]any)["values"].([]any)
	if len(qLevels) != 1 || qLevels[0] != "high" {
		t.Fatalf("仅思考模型应只给一档：%v", qLevels)
	}
	if qwen["config"].(map[string]any)["properties"].(map[string]any)["contextWindow"] != float64(400000) {
		t.Fatal("第二个模型的上下文没写对")
	}
}

// TestWorkBuddyWritesReasoningCapabilities：WorkBuddy 的模型子菜单里出不出现「思考强度」，
// 看的是 supportsReasoning / supportedEfforts / onlyReasoning / canDisableThinking 这几个字段；
// 缺了它们，「档位无法选择」。形状对齐客户端自带的自定义模型编辑器（buildSaveRequest）。
func TestWorkBuddyWritesReasoningCapabilities(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, ".workbuddy-ai")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "models.json")

	if _, err := applyClient(clientByID(t, "workbuddy"), params()); err != nil {
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
	glm := byID["glm-5"]
	if glm["supportsReasoning"] != true || glm["supportsImages"] != true {
		t.Fatalf("glm-5 能力位没写全：%v", glm)
	}
	if _, ok := glm["onlyReasoning"]; ok {
		t.Fatal("可关思考的模型不该写 onlyReasoning")
	}
	// 输入上限取 max_allowed_size（不是上下文）——客户端拿它卡"一次能塞多长"
	if glm["maxInputTokens"] != float64(168000) || glm["maxAllowedSize"] != float64(168000) {
		t.Fatalf("输入上限应取 max_allowed_size：%v", glm)
	}
	if glm["maxOutputTokens"] != float64(128000) || glm["contextLength"] != float64(1000000) {
		t.Fatalf("规格没写：%v", glm)
	}
	reasoning := glm["reasoning"].(map[string]any)
	if reasoning["defaultEffort"] != "high" {
		t.Fatalf("默认档位不对：%v", reasoning)
	}
	efforts := reasoning["supportedEfforts"].([]any)
	if len(efforts) != 3 || efforts[0] != "low" || efforts[2] != "high" {
		t.Fatalf("档位清单不对：%v", efforts)
	}
	if _, ok := reasoning["canDisableThinking"]; ok {
		t.Fatal("能关思考的模型不该写 canDisableThinking")
	}
	qwen := byID["qwen3-max"]
	if qwen["onlyReasoning"] != true {
		t.Fatalf("仅思考模型要写 onlyReasoning：%v", qwen)
	}
	if qwen["reasoning"].(map[string]any)["canDisableThinking"] != false {
		t.Fatal("仅思考模型要显式 canDisableThinking:false")
	}
	if _, ok := qwen["supportsImages"]; !ok || qwen["supportsImages"] != false {
		t.Fatalf("站点报 false 的图片能力要写 false：%v", qwen)
	}

	// 站点改口（模型不再支持思考 / 不再报规格）⇒ 旧元数据必须清掉，不能留在配置里
	p := params()
	p.Catalog[0].SupportsReasoning = bp(false)
	p.Catalog[0].ContextLength = nil
	p.Catalog[0].MaxOutputTokens = nil
	p.Catalog[0].MaxAllowedSize = nil
	if _, err := applyClient(clientByID(t, "workbuddy"), p); err != nil {
		t.Fatal(err)
	}
	arr = nil
	if err := json.Unmarshal([]byte(readText(path)), &arr); err != nil {
		t.Fatal(err)
	}
	for _, m := range arr {
		if m["id"] != "glm-5" {
			continue
		}
		for _, k := range []string{"supportsReasoning", "onlyReasoning", "reasoning",
			"maxInputTokens", "maxAllowedSize", "maxOutputTokens", "contextLength"} {
			if _, ok := m[k]; ok {
				t.Fatalf("失活的字段 %s 没删掉：%v", k, m)
			}
		}
	}
}

// TestClientCatalogsCarryOfficialColumns：官网清单那几列，凡客户端 schema 里有的位置都要填上——
// OpenCode 的 limit/modalities/reasoning、OpenClaw 的 contextWindow/maxTokens/input/reasoning、
// Kimi 的 max_context_size、Grok 的 context_window、Codex 的 context_window/档位。
func TestClientCatalogsCarryOfficialColumns(t *testing.T) {
	home := sandbox(t)
	p := params()

	// --- OpenCode ---
	mustApply(t, "opencode")
	oc := readJSON(opencodeConfigPath(home))
	models := oc["provider"].(map[string]any)["aster"].(map[string]any)["models"].(map[string]any)
	glm := models["glm-5"].(map[string]any)
	limit := glm["limit"].(map[string]any)
	if limit["context"] != float64(1000000) || limit["output"] != float64(128000) || limit["input"] != float64(168000) {
		t.Fatalf("OpenCode limit 没按官网列写：%v", limit)
	}
	mods := glm["modalities"].(map[string]any)["input"].([]any)
	if len(mods) != 2 || mods[1] != "image" {
		t.Fatalf("OpenCode 输入模态不对：%v", mods)
	}
	if glm["reasoning"] != true {
		t.Fatal("OpenCode reasoning 没写")
	}

	// --- OpenClaw ---
	mustApply(t, "openclaw")
	root := readJSON(filepath.Join(home, ".openclaw", "openclaw.json"))
	list := root["models"].(map[string]any)["providers"].(map[string]any)["aster"].(map[string]any)["models"].([]any)
	first := list[0].(map[string]any)
	if first["contextWindow"] != float64(1000000) || first["maxTokens"] != float64(128000) || first["reasoning"] != true {
		t.Fatalf("OpenClaw 规格没写：%v", first)
	}
	if in := first["input"].([]any); len(in) != 2 || in[1] != "image" {
		t.Fatalf("OpenClaw 输入模态不对：%v", in)
	}

	// --- Kimi Code ---
	mustApply(t, "kimi")
	kimi := readText(filepath.Join(kimiHome(home), "config.toml"))
	if !strings.Contains(kimi, "max_context_size = 1000000") {
		t.Fatalf("Kimi 应写站点真上下文（不是写死 20 万）：\n%s", kimi)
	}

	// --- Grok Build ---
	mustApply(t, "grok")
	grok := readText(filepath.Join(grokHome(home), "config.toml"))
	if !strings.Contains(grok, "context_window = 1000000") {
		t.Fatalf("Grok 应写站点真上下文：\n%s", grok)
	}

	// --- Codex ---
	mustApply(t, "codex")
	catalog := readJSON(filepath.Join(codexHome(home), catalogFile))
	entries := catalog["models"].([]any)
	entry := entries[0].(map[string]any)
	if entry["context_window"] != float64(1000000) || entry["max_context_window"] != float64(1000000) {
		t.Fatalf("Codex 上下文没写：%v", entry)
	}
	if mods := entry["input_modalities"].([]any); len(mods) != 2 || mods[1] != "image" {
		t.Fatalf("Codex 输入模态不对：%v", mods)
	}
	levels := entry["supported_reasoning_levels"].([]any)
	if len(levels) != 3 {
		t.Fatalf("Codex 档位清单不对：%v", levels)
	}
	if entry["default_reasoning_level"] != "high" || entry["default_reasoning_summary"] != "auto" {
		t.Fatalf("Codex 默认档位/摘要不对：%v", entry)
	}
	_ = p
}

// TestFetchModelsCarriesCapabilities：能力字段（思考档位/媒体/上下文）也必须一路带回前端，
// 否则写出来的覆盖层是空的——ZCode 没档位、WorkBuddy 没思考强度都是这个链路上丢的。
func TestFetchModelsCarriesCapabilities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"space-bunny","supports_reasoning":true,"only_reasoning":true,
			"reasoning_supported_efforts":["low","medium","high","xhigh","max"],"reasoning_default_effort":"max",
			"supports_images":true,"supports_video":true,"context_length":1000000,"max_output_tokens":128000}]}`)
	}))
	defer srv.Close()

	items, err := fetchModels(srv.URL, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	m := items[0]
	if m.SupportsReasoning == nil || !*m.SupportsReasoning || m.OnlyReasoning == nil || !*m.OnlyReasoning {
		t.Fatalf("思考能力位没带回来：%+v", m)
	}
	if len(m.ReasoningEfforts) != 5 || m.DefaultEffort != "max" {
		t.Fatalf("档位清单没带回来：%+v", m)
	}
	if m.SupportsVideo == nil || !*m.SupportsVideo || m.ContextLength == nil || *m.ContextLength != 1000000 {
		t.Fatalf("媒体/上下文没带回来：%+v", m)
	}
}

// 站点没报的能力字段必须是 nil（不写覆盖层），不能当 false 写下去——
// 否则「站点没数据」会被客户端当成「明确不支持」，把入口关掉。
func TestFetchModelsLeavesAbsentFieldsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"media-model","credits":"≈83 积分/秒"}]}`)
	}))
	defer srv.Close()

	items, err := fetchModels(srv.URL, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	m := items[0]
	if m.SupportsReasoning != nil || m.SupportsImages != nil || m.ContextLength != nil {
		t.Fatalf("没报的字段应为 nil：%+v", m)
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
