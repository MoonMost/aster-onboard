package main

// 第二批客户端（DSH / Pi / MiniMax Code / Qwen Code / CodeBuddy）的写入器用例。
// 重点两条：① 字段口径（与门户导出逐项一致）；② 「只动自己那一段」——别的 provider、
// 别人的条目、用户自己选的默认都不许被碰。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// findSeqEntry：顶层条目数组里按 id 找一条，转成 map（DSH 的 cordis.patch.yml 形态）。
func findSeqEntry(t *testing.T, src, id string) map[string]any {
	t.Helper()
	var seq []map[string]any
	if err := yaml.Unmarshal([]byte(src), &seq); err != nil {
		t.Fatalf("YAML 解析失败：%v\n%s", err, src)
	}
	for _, item := range seq {
		if item["id"] == id {
			return item
		}
	}
	t.Fatalf("没找到条目 %s：\n%s", id, src)
	return nil
}

func mapAt(t *testing.T, root map[string]any, keys ...string) map[string]any {
	t.Helper()
	cur := root
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			t.Fatalf("键 %v 不存在/不是映射：%v", keys, cur)
		}
		cur = next
	}
	return cur
}

// withMedia：在标准 params 上追加一条媒体行，用来验证「媒体进/不进清单」的取舍。
func withMedia() Params {
	p := params()
	p.Models = append(p.Models, "minimax-h3")
	p.Catalog = append(p.Catalog, catalogItem{ID: "minimax-h3", Type: "video"})
	return p
}

// numIs：JSON 读回来的数字是 float64、YAML 是 int——断言统一走这里。
func numIs(v any, want int64) bool {
	switch n := v.(type) {
	case float64:
		return int64(n) == want
	case int:
		return int64(n) == want
	case int64:
		return n == want
	}
	return false
}

func TestDshCreatesPatchAndCredentials(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, ".dsh", "profiles", "desktop")
	os.MkdirAll(dir, 0o755)
	// 真机上 profiles/ 下还有 node_modules（共享依赖目录）与点开头目录——都不是 profile，
	// 不许把 patch 写进去。
	os.MkdirAll(filepath.Join(home, ".dsh", "profiles", "node_modules"), 0o755)
	os.MkdirAll(filepath.Join(home, ".dsh", "profiles", ".tmp"), 0o755)
	os.WriteFile(filepath.Join(dir, "cordis.patch.yml"),
		[]byte("- id: locale\n  name: \"@deepseek-ai/dsh-client-locale\"\n  config:\n    preference: zh\n"), 0o644)

	if _, err := applyClient(clientByID(t, "dsh"), withMedia()); err != nil {
		t.Fatalf("dsh 写入失败：%v", err)
	}
	patch := readText(filepath.Join(dir, "cordis.patch.yml"))
	// 别人的条目原样保留
	findSeqEntry(t, patch, "locale")
	entry := findSeqEntry(t, patch, "llm-pi-ai")
	profile := mapAt(t, entry, "config", "providers", "aster")
	if profile["displayName"] != providerDisplay || profile["api"] != "openai-completions" {
		t.Fatalf("provider 基本字段不对：%v", profile)
	}
	if profile["baseURL"] != "https://relay.example.top/v1" || profile["apiKeyEnv"] != apiKeyRef {
		t.Fatalf("baseURL/apiKeyEnv 不对：%v", profile)
	}
	if mapAt(t, profile, "compat")["supportsDeveloperRole"] != false {
		t.Fatal("compat.supportsDeveloperRole 应为 false")
	}
	models, _ := profile["models"].([]any)
	if len(models) != 3 {
		t.Fatalf("模型数应为 3（两条聊天 + 一条媒体）：%v", models)
	}
	first, _ := models[0].(map[string]any)
	if fmt.Sprint(first["contextWindow"]) != "1000000" || fmt.Sprint(first["maxTokens"]) != "384000" {
		t.Fatalf("glm-5 的 contextWindow/maxTokens 口径不对（后者应取组内可用最高 384000）：%v", first)
	}
	if fmt.Sprint(first["input"]) != "[text image]" {
		t.Fatalf("支持图片的模型要写 input: [text, image]：%v", first["input"])
	}
	efforts, _ := first["reasoningEfforts"].(map[string]any)
	if fmt.Sprint(efforts) != "map[high:high low:low medium:medium]" {
		t.Fatalf("reasoningEfforts 应原样映射三档：%v", efforts)
	}
	// 媒体行：占位上下文、不该有 reasoningEfforts
	media, _ := models[2].(map[string]any)
	if fmt.Sprint(media["contextWindow"]) != fmt.Sprint(int64(MEDIA_CONTEXT_FALLBACK)) {
		t.Fatalf("媒体行应用占位上下文：%v", media)
	}
	// 默认模型条目：新建时指向 aster
	def := findSeqEntry(t, patch, "agent-default-model")
	cfg := mapAt(t, def, "config")
	if cfg["provider"] != providerID || cfg["model"] != "glm-5" {
		t.Fatalf("agent-default-model 不对：%v", cfg)
	}
	// 凭据：version 1 + refs.ASTER_API_KEY
	cred := readText(filepath.Join(home, ".dsh", ".credentials.yaml"))
	var credRoot map[string]any
	if err := yaml.Unmarshal([]byte(cred), &credRoot); err != nil {
		t.Fatalf("凭据 YAML 解析失败：%v\n%s", err, cred)
	}
	if fmt.Sprint(credRoot["version"]) != "1" {
		t.Fatalf("凭据版本应为数字 1（带引号会被 DSH 拒）：%v", credRoot["version"])
	}
	if mapAt(t, credRoot, "refs")[apiKeyRef] != "sk-test" {
		t.Fatalf("凭据 refs 不对：%v", credRoot["refs"])
	}
	// 没跑过的 web profile 不许被凭空建出来
	if _, err := os.Stat(filepath.Join(home, ".dsh", "profiles", "web")); err == nil {
		t.Fatal("没跑过的 web profile 被凭空建出来了")
	}
	// node_modules / 点开头目录不是 profile：不许往里写 patch
	if _, err := os.Stat(filepath.Join(home, ".dsh", "profiles", "node_modules", "cordis.patch.yml")); err == nil {
		t.Fatal("patch 被写进了 profiles/node_modules")
	}
	if _, err := os.Stat(filepath.Join(home, ".dsh", "profiles", ".tmp", "cordis.patch.yml")); err == nil {
		t.Fatal("patch 被写进了点开头目录")
	}
}

func TestDshMergesAndKeepsForeign(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, ".dsh", "profiles", "desktop")
	os.MkdirAll(dir, 0o755)
	existing := `- id: llm-pi-ai
  name: "@deepseek-ai/dsh-llm-pi-ai"
  config:
    providers:
      openai:
        apiKeyEnv: OPENAI_API_KEY
        api: openai-completions
        baseURL: https://api.openai.com/v1
        models:
          - id: gpt-5
- id: agent-default-model
  name: "@deepseek-ai/dsh-agent-default-model"
  config:
    provider: deepseek-official
    model: deepseek-v4-flash
    reasoningEffort: high
`
	os.WriteFile(filepath.Join(dir, "cordis.patch.yml"), []byte(existing), 0o644)

	mustApply(t, "dsh")

	patch := readText(filepath.Join(dir, "cordis.patch.yml"))
	entry := findSeqEntry(t, patch, "llm-pi-ai")
	providers := mapAt(t, entry, "config", "providers")
	if _, ok := providers["openai"].(map[string]any); !ok {
		t.Fatalf("同目录下别人的 provider 被碰掉了：%v", providers)
	}
	if _, ok := providers["aster"].(map[string]any); !ok {
		t.Fatalf("aster provider 没并进去：%v", providers)
	}
	// 用户自己选的默认（别家）不许抢
	cfg := mapAt(t, findSeqEntry(t, patch, "agent-default-model"), "config")
	if cfg["provider"] != "deepseek-official" || cfg["reasoningEffort"] != "high" {
		t.Fatalf("用户自己选的默认被改了：%v", cfg)
	}
}

func TestDshCredentialsMigratesFlatLayout(t *testing.T) {
	home := sandbox(t)
	os.MkdirAll(filepath.Join(home, ".dsh"), 0o755)
	os.WriteFile(filepath.Join(home, ".dsh", ".credentials.yaml"), []byte("OLD_KEY: abc\n"), 0o644)

	mustApply(t, "dsh")

	var credRoot map[string]any
	cred := readText(filepath.Join(home, ".dsh", ".credentials.yaml"))
	if err := yaml.Unmarshal([]byte(cred), &credRoot); err != nil {
		t.Fatalf("凭据 YAML 解析失败：%v\n%s", err, cred)
	}
	refs := mapAt(t, credRoot, "refs")
	if refs["OLD_KEY"] != "abc" || refs[apiKeyRef] != "sk-test" {
		t.Fatalf("扁平布局迁移不对：%v", refs)
	}
}

func TestPiWritesModelsAndDefaults(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, ".pi", "agent")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "models.json"),
		[]byte(`{"providers":{"anthropic":{"apiKey":"x","models":[{"id":"claude"}]}},"keep":1}`), 0o644)
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"defaultProvider":"anthropic","sessionDir":"s1"}`), 0o644)

	if _, err := applyClient(clientByID(t, "pi"), withMedia()); err != nil {
		t.Fatalf("pi 写入失败：%v", err)
	}

	root := readJSON(filepath.Join(dir, "models.json"))
	providers := mapAt(t, root, "providers")
	if _, ok := providers["anthropic"].(map[string]any); !ok {
		t.Fatalf("别人的 provider 被碰掉了：%v", providers)
	}
	if fmt.Sprint(root["keep"]) != "1" {
		t.Fatal("顶层未知键应保留")
	}
	aster := mapAt(t, providers, "aster")
	if aster["baseUrl"] != "https://relay.example.top/v1" || aster["api"] != "openai-completions" || aster["apiKey"] != "sk-test" {
		t.Fatalf("aster provider 字段不对：%v", aster)
	}
	models, _ := aster["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("媒体行不该进 Pi 清单（应为 2 条）：%v", models)
	}
	first, _ := models[0].(map[string]any)
	if first["reasoning"] != true || !numIs(first["contextWindow"], 1000000) || !numIs(first["maxTokens"], 384000) {
		t.Fatalf("模型条目不对：%v", first)
	}
	settings := readJSON(filepath.Join(dir, "settings.json"))
	if settings["defaultProvider"] != "aster" || settings["defaultModel"] != "glm-5" || settings["sessionDir"] != "s1" {
		t.Fatalf("settings.json 默认/保留不对：%v", settings)
	}
}

func TestMinimaxCodeWritesCustomProvider(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, ".minimax")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("theme: dark\ncustom_provider:\n  mine:\n    name: 别家\n    kind: custom\n    enabled: true\n    options:\n      baseURL: https://x/v1\n      apiKey: k\n    models:\n      m:\n        name: m\n"), 0o644)

	if _, err := applyClient(clientByID(t, "minimax-code"), withMedia()); err != nil {
		t.Fatalf("mcode 写入失败：%v", err)
	}

	src := readText(filepath.Join(dir, "config.yaml"))
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("YAML 解析失败：%v\n%s", err, src)
	}
	if doc["theme"] != "dark" {
		t.Fatal("顶层未知键应保留")
	}
	cp := mapAt(t, doc, "custom_provider")
	if _, ok := cp["mine"].(map[string]any); !ok {
		t.Fatalf("别人的 provider 被碰掉了：%v", cp)
	}
	aster := mapAt(t, cp, "aster")
	if aster["kind"] != "custom" || aster["enabled"] != true || aster["api"] != "openai-completions" {
		t.Fatalf("aster 条目不对：%v", aster)
	}
	opts := mapAt(t, aster, "options")
	if opts["baseURL"] != "https://relay.example.top/v1" || opts["apiKey"] != "sk-test" {
		t.Fatalf("options 不对：%v", opts)
	}
	models := mapAt(t, aster, "models")
	if len(models) != 2 {
		t.Fatalf("媒体行不该进 mcode 清单（应为 2 条）：%v", models)
	}
	glm := mapAt(t, models, "glm-5")
	if glm["reasoning"] != true {
		t.Fatalf("思考标记不对：%v", glm)
	}
	limit := mapAt(t, glm, "limit")
	if fmt.Sprint(limit["context"]) != "1000000" || fmt.Sprint(limit["output"]) != "384000" {
		t.Fatalf("limit 口径不对：%v", limit)
	}
}

func TestQwenWritesSettingsAndEnv(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, ".qwen")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"modelProviders":{"openai":[{"id":"mine","name":"mine","baseUrl":"https://x/v1","envKey":"OTHER_KEY","wireApi":"chat-completions"}]},"theme":"dark"}`), 0o644)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("OTHER=1\n"), 0o644)

	if _, err := applyClient(clientByID(t, "qwen"), withMedia()); err != nil {
		t.Fatalf("qwen 写入失败：%v", err)
	}

	root := readJSON(filepath.Join(dir, "settings.json"))
	if root["theme"] != "dark" {
		t.Fatal("顶层未知键应保留")
	}
	if mapAt(t, root, "security", "auth")["selectedType"] != "openai" {
		t.Fatal("security.auth.selectedType 应为 openai")
	}
	if mapAt(t, root, "model")["name"] != "glm-5" {
		t.Fatalf("默认模型不对：%v", root["model"])
	}
	list, _ := mapAt(t, root, "modelProviders")["openai"].([]any)
	if len(list) != 4 {
		t.Fatalf("应保留别人的 1 条 + 我们的 3 条（含媒体）：%v", list)
	}
	var ours, media int
	for _, item := range list {
		m, _ := item.(map[string]any)
		if m["envKey"] != apiKeyRef {
			continue
		}
		ours++
		if fmt.Sprint(m["name"]) == "minimax-h3（生成类 · 走 chat 出片）" {
			media++
		}
		if m["baseUrl"] != "https://relay.example.top/v1" || m["wireApi"] != "chat-completions" {
			t.Fatalf("我们的条目字段不对：%v", m)
		}
	}
	if ours != 3 || media != 1 {
		t.Fatalf("我们的条目数不对（应 3 条、其中媒体 1）：ours=%d media=%d", ours, media)
	}
	env := readText(filepath.Join(dir, ".env"))
	if !strings.Contains(env, "ASTER_API_KEY=sk-test") || !strings.Contains(env, "OTHER=1") {
		t.Fatalf(".env 内容不对：%q", env)
	}
}

func TestCodeBuddyWritesModels(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, ".codebuddy")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "models.json"),
		[]byte(`{"models":[{"id":"mine","vendor":"Other","url":"https://x/chat/completions"}]}`), 0o644)

	if _, err := applyClient(clientByID(t, "codebuddy"), withMedia()); err != nil {
		t.Fatalf("codebuddy 写入失败：%v", err)
	}

	root := readJSON(filepath.Join(dir, "models.json"))
	list, _ := root["models"].([]any)
	if len(list) != 4 {
		t.Fatalf("应保留别人的 1 条 + 我们的 3 条（含媒体）：%v", list)
	}
	first, _ := list[0].(map[string]any)
	if first["vendor"] != "Other" {
		t.Fatalf("别人的条目应原样保留在最前：%v", first)
	}
	var ours, media int
	for _, item := range list {
		m, _ := item.(map[string]any)
		if m["vendor"] != codebuddyVendor {
			continue
		}
		ours++
		if fmt.Sprint(m["url"]) != "https://relay.example.top/v1/chat/completions" {
			t.Fatalf("url 必须是完整路径、以 /chat/completions 结尾：%v", m)
		}
		if fmt.Sprint(m["name"]) == "minimax-h3（生成类 · 走 chat 出片）" {
			media++
			if !numIs(m["maxInputTokens"], MEDIA_CONTEXT_FALLBACK) {
				t.Fatalf("媒体行占位输入上限不对：%v", m)
			}
		}
	}
	if ours != 3 || media != 1 {
		t.Fatalf("我们的条目数不对（应 3 条、其中媒体 1）：ours=%d media=%d", ours, media)
	}
	glm, _ := list[1].(map[string]any)
	if !numIs(glm["maxInputTokens"], 168000) || !numIs(glm["maxOutputTokens"], 384000) {
		t.Fatalf("glm-5 上限口径不对（输入取 max_allowed_size、输出取组内可用最高）：%v", glm)
	}
	if glm["supportsImages"] != true || glm["supportsReasoning"] != true {
		t.Fatalf("能力声明不对：%v", glm)
	}
}

func TestChatboxWritesProvider(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, "AppData", "Roaming", "xyz.chatboxapp.app")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{
  "settings": {
    "__version": 6,
    "theme": "dark",
    "customProviders": [{"id":"custom-provider-other","name":"别家","type":"openai","isCustom":true}],
    "providers": {"custom-provider-other":{"apiHost":"https://x/v1","apiPath":"/chat/completions","apiKey":"k","models":[{"modelId":"m"}]}}
  },
  "configVersion": 3
}`), 0o644)

	if _, err := applyClient(clientByID(t, "chatbox"), withMedia()); err != nil {
		t.Fatalf("chatbox 写入失败：%v", err)
	}

	root := readJSON(filepath.Join(dir, "config.json"))
	settings := mapAt(t, root, "settings")
	if fmt.Sprint(settings["__version"]) != "6" || settings["theme"] != "dark" {
		t.Fatalf("既有 settings 键被碰：%v", settings)
	}
	if fmt.Sprint(root["configVersion"]) != "3" {
		t.Fatal("顶层未知键应保留")
	}
	cps, _ := settings["customProviders"].([]any)
	if len(cps) != 2 {
		t.Fatalf("customProviders 应保留别人的 1 条 + 我们的 1 条：%v", cps)
	}
	var ours map[string]any
	for _, item := range cps {
		if m, _ := item.(map[string]any); m["id"] == chatboxProviderID {
			ours = m
		}
	}
	if ours == nil || ours["isCustom"] != true || ours["type"] != "openai" || ours["name"] != providerDisplay {
		t.Fatalf("我们的 customProvider 条目不对：%v", ours)
	}
	provider := mapAt(t, ensureObjectForTest(settings, "providers"), chatboxProviderID)
	if provider["apiHost"] != "https://relay.example.top/v1" || provider["apiPath"] != "/chat/completions" || provider["apiKey"] != "sk-test" {
		t.Fatalf("provider 凭据/地址不对：%v", provider)
	}
	models, _ := provider["models"].([]any)
	if len(models) != 3 {
		t.Fatalf("模型数应为 3（含媒体）：%v", models)
	}
	first, _ := models[0].(map[string]any)
	if !numIs(first["contextWindow"], 1000000) || !numIs(first["maxOutput"], 384000) {
		t.Fatalf("上下文/输出口径不对：%v", first)
	}
	caps := fmt.Sprint(first["capabilities"])
	if !strings.Contains(caps, "vision") || !strings.Contains(caps, "reasoning") || !strings.Contains(caps, "tool_use") {
		t.Fatalf("能力声明不对：%v", caps)
	}
	media, _ := models[2].(map[string]any)
	if !strings.Contains(fmt.Sprint(media["nickname"]), "生成类") {
		t.Fatalf("媒体行应带后缀别名：%v", media)
	}
	def := mapAt(t, settings, "defaultChatModel")
	if def["provider"] != chatboxProviderID || def["model"] != "glm-5" {
		t.Fatalf("defaultChatModel 不对：%v", def)
	}
	if !clientByID(t, "chatbox").Managed(home) {
		t.Fatal("写入后应判定为已托管")
	}
}

// ensureObjectForTest：测试里读回嵌套映射（存在即取，不存在造一个，避免 nil 崩溃）。
func ensureObjectForTest(root map[string]any, key string) map[string]any {
	if m, ok := root[key].(map[string]any); ok {
		return m
	}
	m := map[string]any{}
	root[key] = m
	return m
}

func TestChatboxRejectsLegacyFlatConfig(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, "AppData", "Roaming", "xyz.chatboxapp.app")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"settings":{"aiProvider":"openai","theme":"dark"}}`), 0o644)

	_, err := applyClient(clientByID(t, "chatbox"), params())
	if err == nil || !strings.Contains(err.Error(), "老格式") {
		t.Fatalf("老格式配置应报错并说清怎么办，得到：%v", err)
	}
}

func TestTraeWritesModels(t *testing.T) {
	home := sandbox(t)
	dir := filepath.Join(home, "AppData", "Roaming", "trae_cli")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "trae_cli.yaml"), []byte("theme: dark\nmodels:\n  - name: \"别家的\"\n    open_ai:\n      base_url: https://other.example/v1\n      api_key: other-key\n      model: m\n"), 0o644)

	if _, err := applyClient(clientByID(t, "trae"), withMedia()); err != nil {
		t.Fatalf("trae 写入失败：%v", err)
	}
	// 再写一次：认领式重建应当是幂等的
	if _, err := applyClient(clientByID(t, "trae"), withMedia()); err != nil {
		t.Fatalf("trae 二次写入失败：%v", err)
	}

	src := readText(filepath.Join(dir, "trae_cli.yaml"))
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("YAML 解析失败：%v\n%s", err, src)
	}
	if doc["theme"] != "dark" {
		t.Fatal("顶层未知键应保留")
	}
	list, _ := doc["models"].([]any)
	if len(list) != 3 {
		t.Fatalf("应保留别人的 1 条 + 我们的 2 条（媒体不进、二次写入不重复）：%v", list)
	}
	other, _ := list[0].(map[string]any)
	if other["name"] != "别家的" {
		t.Fatalf("别人的条目应原样保留在最前：%v", other)
	}
	for _, item := range list[1:] {
		m, _ := item.(map[string]any)
		if !strings.HasPrefix(fmt.Sprint(m["name"]), traeModelPrefix) {
			t.Fatalf("我们的条目要带前缀：%v", m)
		}
		oa := mapAt(t, m, "open_ai")
		if oa["base_url"] != "https://relay.example.top/v1" || oa["api_key"] != "sk-test" {
			t.Fatalf("open_ai 字段不对：%v", oa)
		}
	}
	if !clientByID(t, "trae").Managed(home) {
		t.Fatal("写入后应判定为已托管")
	}
}
