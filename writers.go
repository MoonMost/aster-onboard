package main

// 各客户端配置写入器。形状逐条对拍自 EasyCLIProxyAPI 的 src-tauri/src/agents/configuration.rs，
// 托管 provider id 换成 "aster"、展示名换成 "Aster"；根地址给 Anthropic 系客户端，/v1 给 OpenAI 系。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

func readText(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

func parseJSONObject(existing, label string) (map[string]any, error) {
	trimmed := strings.TrimSpace(existing)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(stripJSONC([]byte(trimmed)), &root); err != nil {
		return nil, fmt.Errorf("%s 不是合法 JSON：%w", label, err)
	}
	if root == nil {
		return map[string]any{}, nil
	}
	return root, nil
}

func ensureObject(root map[string]any, keys ...string) map[string]any {
	cur := root
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[k] = next
		}
		cur = next
	}
	return cur
}

func renderJSON(root any) string {
	raw, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(raw) + "\n"
}

func orderedModels(p Params) []string {
	seen := map[string]bool{}
	var out []string
	if p.Model != "" {
		out = append(out, p.Model)
		seen[p.Model] = true
	}
	for _, m := range p.Models {
		if !seen[m] {
			out = append(out, m)
			seen[m] = true
		}
	}
	if len(out) == 0 {
		out = []string{p.Model}
	}
	return out
}

// MEDIA_CONTEXT_FALLBACK 媒体行（图像/视频生成）塞进聊天客户端时的上下文占位值：
// 它们没有 token 规格，照实写 0/空容易被客户端当坏条目（2026-09-29 ZCode 同步实测）。
// 与门户「完整配置导出」的 workbuddyMediaEntry、tools/zcode-sync 的 MEDIA_CONTEXT_FALLBACK
// 同一个数——三处口径保持一致，别各写一个数。
// 2026-10-15：32000 → 1M，与 minimax 引擎 mediaContextFallback 同步提档。
const MEDIA_CONTEXT_FALLBACK = 1000000

// agentModels 返回编码 agent 配置里该列的模型清单与默认模型名：媒体行不进编码 agent
// 的清单——与 router 自己的「完整配置导出」同一取舍（这些客户端跑不了生成任务，选了
// 只会把编码请求变成出图轮、按张/秒扣费）。朋友把默认模型选成媒体行时退回清单里第一个
// 非媒体模型；全量都是媒体行的退化情形原样返回，绝不写空清单。
// WorkBuddy 与 ZCode 不走这里：它们是通用桌面端，媒体行能走 chat 出片，照全量写。
func agentModels(p Params) (names []string, def string) {
	all := orderedModels(p)
	names = make([]string, 0, len(all))
	for _, m := range all {
		if it, ok := p.itemOf(m); ok && it.IsMedia() {
			continue
		}
		names = append(names, m)
	}
	if len(names) == 0 {
		return all, p.Model
	}
	def = p.Model
	if it, ok := p.itemOf(def); ok && it.IsMedia() {
		def = names[0]
	}
	return names, def
}

func writeDirFor(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o755)
}

// ---------- Claude Code：~/.claude/settings.json 的 env 块 ----------

func buildClaudeCode(home string, p Params) (map[string]string, error) {
	path := claudeCodeSettingsPath(home)
	root, err := parseJSONObject(readText(path), "Claude Code settings.json")
	if err != nil {
		return nil, err
	}
	env := ensureObject(root, "env")
	delete(env, "ANTHROPIC_API_KEY")
	_, m := agentModels(p)
	for k, v := range map[string]string{
		"ANTHROPIC_BASE_URL":             p.Base,
		"ANTHROPIC_AUTH_TOKEN":           p.Key,
		"ANTHROPIC_MODEL":                m,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  m,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": m,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   m,
	} {
		env[k] = v
	}
	if _, ok := env["CLAUDE_CODE_SUBAGENT_MODEL"]; !ok {
		env["CLAUDE_CODE_SUBAGENT_MODEL"] = m
	}
	// 让 Claude Code 自己向网关拉全量模型清单，而不是只认默认模型
	env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] = "1"
	root["model"] = m
	return map[string]string{path: renderJSON(root)}, nil
}

// ---------- Claude Desktop：四个文件 ----------

func claudeFamilyTier(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "-opus-") || strings.HasPrefix(lower, "claude-opus-"):
		return "opus"
	case strings.Contains(lower, "-sonnet-") || strings.HasPrefix(lower, "claude-sonnet-"):
		return "sonnet"
	case strings.Contains(lower, "-haiku-") || strings.HasPrefix(lower, "claude-haiku-"):
		return "haiku"
	}
	return ""
}

func buildClaudeDesktop(home string, p Params) (map[string]string, error) {
	paths := claudeDesktopPaths(home)
	out := map[string]string{}

	for _, path := range paths[:2] { // 常规与 3p 配置：切到第三方部署模式
		root, err := parseJSONObject(readText(path), "Claude Desktop 配置")
		if err != nil {
			return nil, err
		}
		root["deploymentMode"] = "3p"
		out[path] = renderJSON(root)
	}

	profile, err := parseJSONObject(readText(paths[2]), "Claude Desktop gateway 配置")
	if err != nil {
		return nil, err
	}
	profile["disableDeploymentModeChooser"] = true
	profile["inferenceGatewayApiKey"] = p.Key
	profile["inferenceGatewayAuthScheme"] = "bearer"
	profile["inferenceGatewayBaseUrl"] = p.Base
	profile["inferenceProvider"] = "gateway"
	// 全量清单都进推理模型列表，名字撞 Claude 家族的补档位标记
	seenTier := map[string]bool{}
	names, _ := agentModels(p)
	var inferenceModels []any
	for _, name := range names {
		entry := map[string]any{"name": name}
		if tier := claudeFamilyTier(name); tier != "" {
			entry["anthropicFamilyTier"] = tier
			if !seenTier[tier] {
				entry["isFamilyDefault"] = true
				seenTier[tier] = true
			}
		}
		inferenceModels = append(inferenceModels, entry)
	}
	profile["inferenceModels"] = inferenceModels
	out[paths[2]] = renderJSON(profile)

	meta, err := parseJSONObject(readText(paths[3]), "Claude Desktop 配置索引")
	if err != nil {
		return nil, err
	}
	entries, _ := meta["entries"].([]any)
	var kept []any
	for _, e := range entries {
		m, _ := e.(map[string]any)
		if m == nil {
			continue
		}
		if id, _ := m["id"].(string); id == claudeDesktopProfileID {
			continue
		}
		if _, hasName := m["name"]; !hasName {
			if id, _ := m["id"].(string); id != "" {
				m["name"] = "Configuration " + id
			}
		}
		kept = append(kept, m)
	}
	kept = append(kept, map[string]any{"id": claudeDesktopProfileID, "name": providerName})
	meta["entries"] = kept
	meta["appliedId"] = claudeDesktopProfileID
	out[paths[3]] = renderJSON(meta)
	return out, nil
}

// ---------- Codex：config.toml + auth.json + 模型 catalog ----------

func buildCodex(home string, p Params) (map[string]string, error) {
	dir := codexHome(home)
	configPath := filepath.Join(dir, "config.toml")
	src := readText(configPath)
	names, def := agentModels(p)

	src = tomlSetTop(src, "model_provider", tomlQuote(providerID))
	src = tomlSetTop(src, "model", tomlQuote(def))
	src = tomlSetTop(src, "model_catalog_json", tomlQuote(catalogFile))
	src = tomlSetSection(src, "model_providers."+providerID, [][2]string{
		{"name", tomlQuote(providerName)},
		{"base_url", tomlQuote(p.openaiBase())},
		{"wire_api", tomlQuote("responses")},
		{"experimental_bearer_token", tomlQuote(p.Key)},
	})

	authPath := filepath.Join(dir, "auth.json")
	auth, err := parseJSONObject(readText(authPath), "Codex auth.json")
	if err != nil {
		return nil, err
	}
	delete(auth, "tokens")
	delete(auth, "last_refresh")
	auth["auth_mode"] = "apikey"
	auth["OPENAI_API_KEY"] = p.Key

	catalog := map[string]any{"models": catalogEntries(p, names)}
	return map[string]string{
		configPath:                      src,
		authPath:                        renderJSON(auth),
		filepath.Join(dir, catalogFile): renderJSON(catalog),
	}, nil
}

// catalogEntries 组 Codex 的 model_catalog.json。**字段不是可选的**：Codex 解析这份文件时
// 少一个字段就整份配置加载失败（2026-10-04 用 codex 0.150 的 `features list` 逐个报错试出来的
// 必填集：supported_reasoning_levels / shell_type / visibility / supported_in_api / priority /
// support_verbosity / truncation_policy / experimental_supported_tools，外加
// base_instructions 或 model_messages.instructions_template 二选一）——只写 slug+display_name
// 会让 Codex 连启动都起不来，这就是当时「给 Codex 导入了配置就再也打不开」的根因。
func catalogEntries(p Params, names []string) []any {
	var out []any
	for _, m := range names {
		entry := map[string]any{
			"slug":         m,
			"display_name": m,
			"shell_type":   "unified_exec",
			// visibility 取值 list/hide/none：list = 出现在模型选择器里（参考实现同样把
			// fallback 的 none 改成 list，否则自定义模型在客户端里看不到）。
			"visibility":                   "list",
			"supported_in_api":             true,
			"support_verbosity":            false,
			"supports_parallel_tool_calls": false,
			"priority":                     99,
			"truncation_policy":            map[string]any{"mode": "bytes", "limit": 10000},
			"experimental_supported_tools": []any{},
			"default_reasoning_summary":    "auto",
			"base_instructions":            codexBaseInstructions,
		}
		if item, ok := p.itemOf(m); ok {
			// 上下文/最大上下文：Codex 拿它算上下文预算与自动压缩阈值。
			if v := intOf(item.ContextLength); v > 0 {
				entry["context_window"] = v
				entry["max_context_window"] = v
			}
			// 输入模态：Codex 只认 text/image。
			entry["input_modalities"] = codexModalities(item)
			// 思考档位清单与默认档：Codex 的 /model 选择器据此列档（未知档位会被丢掉）。
			// 这个字段**每条都必须有**（站点没报档位就给空数组）——缺字段会让整份配置加载失败。
			entry["supported_reasoning_levels"] = codexReasoningLevels(item.ReasoningEfforts)
			if e := item.DefaultEffortOf(); e != "" && codexLevelAllowed(e) {
				entry["default_reasoning_level"] = e
			}
		} else {
			entry["input_modalities"] = []any{"text"}
			entry["supported_reasoning_levels"] = []any{}
		}
		out = append(out, entry)
	}
	return out
}

// codexBaseInstructions：Codex 目录条目的必填指令串（缺了 base_instructions 与
// model_messages.instructions_template 会让整份配置加载失败）。这里给一句中性的编码代理
// 说明，不照搬 Codex 自带的长提示词。
const codexBaseInstructions = "You are a coding agent. Be precise, safe, and helpful. " +
	"Follow the user's instructions and use the tools available to you."

// codexModalities：Codex 只认 text/image 两种输入模态。
func codexModalities(item catalogItem) []any {
	out := []any{"text"}
	if item.SupportsImages != nil && *item.SupportsImages {
		out = append(out, "image")
	}
	return out
}

// codexReasoningLevels：Codex 允许的档位集合（与 EasyCLIProxyAPI 的 is_allowed_reasoning_level
// 同表）；站点报的档位先过滤未知值，再转成带说明的对象数组（Codex 两种写法都收）。
func codexReasoningLevels(efforts []string) []any {
	out := []any{} // 必须是数组：nil 会序列化成 null，Codex 直接拒收整份配置
	for _, e := range efforts {
		if !codexLevelAllowed(e) {
			continue
		}
		out = append(out, map[string]any{"effort": e, "description": e + " reasoning effort"})
	}
	return out
}

func codexLevelAllowed(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	}
	return false
}

// ---------- OpenCode：opencode.json(c) ----------

func buildOpenCode(home string, p Params) (map[string]string, error) {
	path := opencodeConfigPath(home)
	existing := readText(path)
	root, err := parseJSONObject(existing, "OpenCode opencode.json")
	if err != nil {
		return nil, err
	}
	if _, ok := root["$schema"]; !ok {
		root["$schema"] = "https://opencode.ai/config.json"
	}
	provider := ensureObject(root, "provider", providerID)
	provider["npm"] = "@ai-sdk/openai-compatible"
	provider["name"] = providerName
	options := ensureObject(provider, "options")
	options["baseURL"] = p.openaiBase()
	options["apiKey"] = p.Key
	names, def := agentModels(p)
	models := map[string]any{}
	for _, m := range names {
		models[m] = opencodeModelEntry(p, m)
	}
	provider["models"] = models
	root["model"] = providerID + "/" + def
	return map[string]string{path: withLeadingComments(existing, renderJSON(root))}, nil
}

// opencodeModelEntry：OpenCode 的模型条目字段（schema 见 opencode.ai/config.json）——
// limit{context,input,output}、modalities.input、reasoning、attachment、tool_call。
// limit 里 context 与 output 都是必填，所以两个都拿到了才写 limit，缺一个就整块不写。
func opencodeModelEntry(p Params, model string) map[string]any {
	entry := map[string]any{"name": model}
	item, ok := p.itemOf(model)
	if !ok {
		return entry
	}
	ctx, out := intOf(item.ContextLength), item.OutCap()
	if ctx > 0 && out > 0 {
		limit := map[string]any{"context": ctx, "output": out}
		if v := intOf(item.MaxAllowedSize); v > 0 {
			limit["input"] = v
		}
		entry["limit"] = limit
	}
	entry["modalities"] = map[string]any{"input": opencodeModalities(item), "output": []any{"text"}}
	if item.SupportsReasoning != nil {
		entry["reasoning"] = *item.SupportsReasoning
	}
	if item.SupportsImages != nil && *item.SupportsImages {
		entry["attachment"] = true
	}
	if item.SupportsToolCall != nil {
		entry["tool_call"] = *item.SupportsToolCall
	}
	return entry
}

// opencodeModalities：OpenCode 的输入模态取值 text/audio/image/video/pdf。
func opencodeModalities(item catalogItem) []any {
	out := []any{"text"}
	if item.SupportsImages != nil && *item.SupportsImages {
		out = append(out, "image")
	}
	if item.SupportsVideo != nil && *item.SupportsVideo {
		out = append(out, "video")
	}
	return out
}

func withLeadingComments(existing, rendered string) string {
	comments := extractJSONCComments(existing)
	if len(comments) == 0 {
		return rendered
	}
	return strings.Join(comments, "\n") + "\n" + rendered
}

// ---------- OpenClaw：openclaw.json ----------

func buildOpenClaw(home string, p Params) (map[string]string, error) {
	path := filepath.Join(home, ".openclaw", "openclaw.json")
	existing := readText(path)
	root, err := parseJSONObject(existing, "OpenClaw openclaw.json")
	if err != nil {
		return nil, err
	}
	models := ensureObject(root, "models")
	if _, ok := models["mode"]; !ok {
		models["mode"] = "merge"
	}
	providers := ensureObject(models, "providers")
	managed := ensureObject(providers, providerID)
	managed["baseUrl"] = p.openaiBase()
	managed["apiKey"] = p.Key
	managed["api"] = "openai-completions"
	names, def := agentModels(p)
	var list []any
	for _, m := range names {
		list = append(list, openclawModelEntry(p, m))
	}
	managed["models"] = list

	defaults := ensureObject(root, "agents", "defaults")
	model := ensureObject(defaults, "model")
	model["primary"] = providerID + "/" + def
	catalog := ensureObject(defaults, "models")
	for key := range catalog {
		if strings.HasPrefix(key, providerID+"/") {
			delete(catalog, key)
		}
	}
	for _, m := range names {
		catalog[providerID+"/"+m] = map[string]any{}
	}
	return map[string]string{path: withLeadingComments(existing, renderJSON(root))}, nil
}

// openclawModelEntry：OpenClaw 的模型条目字段（见 docs.openclaw.ai 自定义 provider）——
// contextWindow（原生上下文）、maxTokens（输出上限）、input（模态数组）、reasoning。
func openclawModelEntry(p Params, model string) map[string]any {
	entry := map[string]any{"id": model, "name": model}
	item, ok := p.itemOf(model)
	if !ok {
		return entry
	}
	if v := intOf(item.ContextLength); v > 0 {
		entry["contextWindow"] = v
	}
	if v := item.OutCap(); v > 0 {
		entry["maxTokens"] = v
	}
	// 输入模态：OpenClaw 文档只写了 text / image（与 OpenCode 的枚举不同，video 未验证⇒不写）。
	in := []any{"text"}
	if item.SupportsImages != nil && *item.SupportsImages {
		in = append(in, "image")
	}
	entry["input"] = in
	if item.SupportsReasoning != nil {
		entry["reasoning"] = *item.SupportsReasoning
	}
	return entry
}

// ---------- Hermes：config.yaml（yaml.Node 往返，注释保留） ----------

func buildHermes(home string, p Params) (map[string]string, error) {
	path := hermesConfigPath(home)
	existing := readText(path)

	var doc yaml.Node
	if strings.TrimSpace(existing) == "" {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	} else if err := yaml.Unmarshal([]byte(existing), &doc); err != nil {
		return nil, fmt.Errorf("Hermes config.yaml 不是合法 YAML：%w", err)
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("Hermes config.yaml 根节点必须是映射")
	}

	names, def := agentModels(p)
	providerModels := map[string]any{}
	for _, m := range names {
		providerModels[m] = map[string]any{}
	}
	var newNode yaml.Node
	if err := newNode.Encode(map[string]any{
		"name":     providerID,
		"base_url": p.openaiBase(),
		"api_key":  p.Key,
		"api_mode": "chat_completions",
		"model":    def,
		"models":   providerModels,
	}); err != nil {
		return nil, err
	}

	seq := yamlMapSeq(root, "custom_providers")
	var kept []*yaml.Node
	for _, item := range seq.Content {
		if yamlMapGet(item, "name") != providerID {
			kept = append(kept, item)
		}
	}
	providerNode := &newNode
	if newNode.Kind == yaml.DocumentNode && len(newNode.Content) > 0 {
		providerNode = newNode.Content[0]
	}
	seq.Content = append(kept, providerNode)

	modelMap := yamlMapMap(root, "model")
	yamlMapSetScalar(modelMap, "default", p.Model)
	yamlMapSetScalar(modelMap, "provider", providerID)

	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	enc.Close()
	return map[string]string{path: sb.String()}, nil
}

func yamlMapGet(mapping *yaml.Node, key string) string {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1].Value
		}
	}
	return ""
}

func yamlMapSetScalar(mapping *yaml.Node, key, value string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1].Value = value
			mapping.Content[i+1].Kind = yaml.ScalarNode
			mapping.Content[i+1].Tag = "!!str"
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: "!!str"})
}

func yamlMapMap(root *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			if root.Content[i+1].Kind == yaml.MappingNode {
				return root.Content[i+1]
			}
			fresh := &yaml.Node{Kind: yaml.MappingNode}
			root.Content[i+1] = fresh
			return fresh
		}
	}
	fresh := &yaml.Node{Kind: yaml.MappingNode}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, fresh)
	return fresh
}

func yamlMapSeq(root *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			if root.Content[i+1].Kind == yaml.SequenceNode {
				return root.Content[i+1]
			}
			fresh := &yaml.Node{Kind: yaml.SequenceNode}
			root.Content[i+1] = fresh
			return fresh
		}
	}
	fresh := &yaml.Node{Kind: yaml.SequenceNode}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, fresh)
	return fresh
}

// ---------- ZCode：~/.zcode/v2/provider_config.json ----------

func buildZCode(home string, p Params) (map[string]string, error) {
	path := filepath.Join(home, ".zcode", "v2", "provider_config.json")
	root, err := parseJSONObject(readText(path), "ZCode provider_config.json")
	if err != nil {
		return nil, err
	}
	if v, ok := root["schemaVersion"]; ok {
		if f, ok := v.(float64); ok && f != 1 {
			return nil, fmt.Errorf("ZCode provider_config.json 的 schemaVersion 不受支持")
		}
	}
	root["schemaVersion"] = 1
	config := ensureObject(root, "config")
	providerRules := ensureObject(config, "providerConfigRules")
	rules, _ := providerRules["providerRules"].([]any)

	names := orderedModels(p)
	managed := map[string]any{
		"providerId":   providerID,
		"providerName": providerName,
		"enabled":      true,
		"config": map[string]any{
			"group": "standard-personal",
			"access": map[string]any{
				"type":   "api-key",
				"apiKey": p.Key,
			},
			// 走 OpenAI 协议（站里所有引擎的原生形状；zcode-sync 给自建 provider 也是这一条）。
			// 不用 anthropic-messages 的原因：那条路的档位是 output_config.effort，而本站
			// /v1/messages 翻译层按红线丢弃 thinking、也不认 output_config ⇒ 档位会变成摆设。
			// OpenAI 路上档位映射成 reasoning_effort + thinking.type（ZCode 内置 api 规则），
			// 正是网关会归一化并透传给引擎的字段。
			"api": map[string]any{
				"type":    "openai-chat-completions",
				"baseUrl": p.openaiBase(),
			},
			"personalModelIds": toAnySlice(names),
			"modelOrder":       toAnySlice(names),
		},
	}
	var kept []any
	insertAt := len(rules)
	for i, r := range rules {
		m, _ := r.(map[string]any)
		if m != nil && m["providerId"] == providerID {
			insertAt = i
			continue
		}
		kept = append(kept, r)
	}
	if insertAt > len(kept) {
		insertAt = len(kept)
	}
	kept = append(kept[:insertAt], append([]any{managed}, kept[insertAt:]...)...)
	providerRules["providerRules"] = kept

	if order, ok := config["providerOrder"].([]any); ok {
		found := false
		for _, v := range order {
			if v == providerID {
				found = true
			}
		}
		if !found {
			config["providerOrder"] = append(order, providerID)
		}
	}
	config["defaultModelSelection"] = map[string]any{
		"providerId": providerID,
		"modelId":    p.Model,
	}
	applyZCodeModelRules(config, p)
	return map[string]string{path: renderJSON(root)}, nil
}

// applyZCodeModelRules 往 ZCode 的**每模型覆盖层** modelConfigRules.providerModelRules
// 写三个键（与 tools/zcode-sync 的 applyModelRulesLayer 同一口径，两个入口都同步时不会互相打架）：
//
//	properties.inputFormat          —— 输入类型。ZCode 自带一张按模型名正则猜能力的内置表，
//	                                   我们这些自定义名（Space-Bunny 之类）撞不上任何正则，
//	                                   会退回「只有文本 / 思考两档 / 上下文 20 万」的兜底条款。
//	properties.contextWindow        —— 上下文窗口（引擎报多少写多少，真让客户端读到 1M）。
//	optionSpecs.reasoningLevel      —— 思考档位下拉的**唯一数据源**；不写就只有「开启/关闭」，
//	                                   写了才有 低/中/高/超高/极致 五档可选。
//
// 只动这三个键，enabled 与其余属性（webSearch / jsonSchema / 手设项）一律不碰。
func applyZCodeModelRules(config map[string]any, p Params) {
	mcr := ensureObject(config, "modelConfigRules")
	rules, _ := mcr["providerModelRules"].([]any)
	for _, name := range orderedModels(p) {
		item, ok := p.itemOf(name)
		if !ok {
			continue
		}
		var rule map[string]any
		for _, r := range rules {
			m, _ := r.(map[string]any)
			if m != nil && m["providerId"] == providerID && m["modelId"] == name {
				rule = m
				break
			}
		}
		if rule == nil {
			rule = map[string]any{"providerId": providerID, "modelId": name}
			rules = append(rules, rule)
		}
		c := ensureObject(rule, "config")
		props := ensureObject(c, "properties")
		inputs := ensureObject(props, "inputFormat")
		inputs["supportsText"] = true
		if item.SupportsImages != nil {
			inputs["supportsImage"] = *item.SupportsImages
		}
		if item.SupportsVideo != nil {
			inputs["supportsVideo"] = *item.SupportsVideo
		}
		// 媒体行没有 token 规格：与门户导出/zcode-sync 同款占位（1M，2026-10-15 提档；空着客户端当坏条目）。
		if item.IsMedia() {
			props["contextWindow"] = MEDIA_CONTEXT_FALLBACK
		} else if item.ContextLength != nil && *item.ContextLength > 0 {
			props["contextWindow"] = *item.ContextLength
		}
		// 档位：引擎声明了几档就写几档；声明了思考但没给清单的按 sync 脚本口径给 ["high"]；
		// 完全不是思考模型给 ["none"]（客户端要一个非空数组，空数组会被校验拒掉）。
		// 不擅自加 "disabled"——「仅思考」的模型关不掉思考，加一档等于对客户端谎报能关。
		levels := []string{}
		only := item.OnlyReasoning != nil && *item.OnlyReasoning
		for _, level := range item.ReasoningEfforts {
			if only && (level == "none" || level == "disabled") {
				continue
			}
			levels = append(levels, level)
		}
		if len(levels) == 0 {
			if only || (item.SupportsReasoning != nil && *item.SupportsReasoning) {
				levels = []string{"high"}
			} else if item.SupportsReasoning != nil || item.IsMedia() {
				levels = []string{"none"}
			}
		}
		specs := ensureObject(c, "optionSpecs")
		if len(levels) > 0 {
			ensureObject(specs, "reasoningLevel")["values"] = toAnySlice(levels)
		}
		// 输出上限：客户端据此知道单条回复最多能写多少 token（内置 api 规则映射成
		// max_completion_tokens）。取**组内可用最高**（item.OutCap()）而不是官网那列的最小——
		// 那列是最弱成员的承诺值，照它写会把 384K 的模型卡在 32K。站点没报就不写。
		if v := item.OutCap(); v > 0 {
			ensureObject(specs, "maxOutputTokens")["max"] = v
		}
	}
	mcr["providerModelRules"] = rules
}

func toAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

// ---------- WorkBuddy：models.json（数组或对象两种形态都收） ----------

func buildWorkBuddy(home string, p Params) (map[string]string, error) {
	path := filepath.Join(workbuddyHome(home), "models.json")
	existing := strings.TrimSpace(readText(path))
	var root any = []any{}
	if existing != "" {
		if err := json.Unmarshal([]byte(existing), &root); err != nil {
			return nil, fmt.Errorf("WorkBuddy models.json 不是合法 JSON：%w", err)
		}
	}
	isArray := false
	if _, ok := root.([]any); ok {
		isArray = true
	}

	all := workbuddyModels(root)
	names := orderedModels(p)
	inCatalog := map[string]bool{}
	for _, n := range names {
		inCatalog[n] = true
	}
	for _, m := range all {
		id, _ := m["id"].(string)
		if m["vendor"] != providerID && inCatalog[strings.TrimPrefix(id, "custom-local:")] {
			return nil, fmt.Errorf("WorkBuddy 里已有同名自定义模型 %s，请先在 WorkBuddy 里改名", id)
		}
	}

	// 自定义模型原样保留；托管条目按全量清单一模型一条重建
	var entries []any
	for _, m := range all {
		if m["vendor"] != providerID {
			entries = append(entries, m)
		}
	}
	for _, name := range names {
		entry := map[string]any{}
		for _, m := range all {
			id, _ := m["id"].(string)
			if m["vendor"] == providerID && strings.TrimPrefix(id, "custom-local:") == name {
				entry = m
				break
			}
		}
		fillWorkBuddyModel(entry, p, name)
		entries = append(entries, entry)
	}

	if isArray {
		root = entries
	} else {
		obj := root.(map[string]any)
		if len(entries) == 0 {
			delete(obj, "models")
		} else {
			obj["models"] = entries
		}
		if visible, ok := obj["availableModels"].([]any); ok && len(visible) > 0 {
			seen := map[string]bool{}
			for _, v := range visible {
				if s, ok := v.(string); ok {
					seen[strings.TrimPrefix(s, "custom-local:")] = true
				}
			}
			for _, name := range names {
				if !seen[name] {
					visible = append(visible, name)
				}
			}
			obj["availableModels"] = visible
		}
		root = obj
	}
	return map[string]string{path: renderJSON(root)}, nil
}

func fillWorkBuddyModel(entry map[string]any, p Params, model string) {
	entry["id"] = model
	entry["name"] = providerName
	entry["vendor"] = providerID
	entry["url"] = p.openaiBase() + "/chat/completions"
	entry["apiKey"] = p.Key
	entry["supportsToolCall"] = true
	entry["disabled"] = false
	entry["useCustomProtocol"] = false
	// 倍率提示与官网那一格同值（见 Params.rateHint）。站点没下发就删掉旧值，
	// 免得把上一次写的倍率留在配置里。
	if hint := p.rateHint(model); hint != "" {
		entry["credits"] = hint
	} else {
		delete(entry, "credits")
	}
	fillWorkBuddyCapabilities(entry, p, model)
}

// fillWorkBuddyCapabilities 写「思考档位 / 媒体输入 / 规格」——形状对齐门户「完整配置导出」
// 的 buildWorkbuddyExport（那是给朋友手贴的同一份东西），也就是客户端设置页那个
// 「自定义模型」编辑器保存时写的字段超集：
//
//	supportsReasoning + onlyReasoning + reasoning{defaultEffort, supportedEfforts, canDisableThinking}
//	supportsImages / supportsVideos
//	maxInputTokens(输入上限=max_allowed_size) / maxAllowedSize / maxOutputTokens / contextLength(上下文)
//
// 缺了能力位，输入框的模型子菜单里就没有「思考强度」一节（isReasoningConfigurable 要求
// supportsReasoning 为真、且要么有 supportedEfforts、要么允许开关），档位自然选不了；
// 缺了 maxInputTokens，客户端也不知道能塞多长的上下文。站点没报的字段一律删掉，不编造能力。
func fillWorkBuddyCapabilities(entry map[string]any, p Params, model string) {
	item, ok := p.itemOf(model)
	if !ok {
		return
	}
	if item.SupportsReasoning != nil && *item.SupportsReasoning {
		entry["supportsReasoning"] = true
		only := item.OnlyReasoning != nil && *item.OnlyReasoning
		if only {
			entry["onlyReasoning"] = true
		} else {
			delete(entry, "onlyReasoning")
		}
		reasoning := map[string]any{}
		if e := item.DefaultEffortOf(); e != "" {
			reasoning["defaultEffort"] = e
		}
		if len(item.ReasoningEfforts) > 0 {
			reasoning["supportedEfforts"] = toAnySlice(item.ReasoningEfforts)
		}
		if only {
			// 「仅思考」的模型关不掉思考——只有 false 时才是显式声明，缺省即允许关闭。
			reasoning["canDisableThinking"] = false
		}
		if len(reasoning) > 0 {
			entry["reasoning"] = reasoning
		} else {
			delete(entry, "reasoning")
		}
	} else {
		delete(entry, "supportsReasoning")
		delete(entry, "onlyReasoning")
		delete(entry, "reasoning")
	}
	setBoolField(entry, "supportsImages", item.SupportsImages)
	setBoolField(entry, "supportsVideos", item.SupportsVideo)
	if item.IsMedia() {
		// 媒体行（图像/视频生成）没有 token 规格：照实空着容易被 WorkBuddy 当坏条目
		// （2026-09-29 实测），按门户导出 workbuddyMediaEntry 与 tools/zcode-sync 的同款
		// 占位写——三处口径一致，别各写一个数。输出上限没有就不写（客户端不读它）。
		entry["maxInputTokens"] = MEDIA_CONTEXT_FALLBACK
		entry["contextLength"] = MEDIA_CONTEXT_FALLBACK
		delete(entry, "maxAllowedSize")
		delete(entry, "maxOutputTokens")
		return
	}
	// 输入上限（max_allowed_size）是客户端真正拿来卡"一次能塞多长"的数；站点没报才退回上下文。
	if v := firstInt(item.MaxAllowedSize, item.ContextLength); v > 0 {
		entry["maxInputTokens"] = v
	} else {
		delete(entry, "maxInputTokens")
	}
	if v := intOf(item.MaxAllowedSize); v > 0 {
		entry["maxAllowedSize"] = v
	} else {
		delete(entry, "maxAllowedSize")
	}
	if v := item.OutCap(); v > 0 {
		entry["maxOutputTokens"] = v
	} else {
		delete(entry, "maxOutputTokens")
	}
	// contextLength 与门户导出同形（客户端当前不读它，留着不碍事，口径对齐在先）。
	if v := intOf(item.ContextLength); v > 0 {
		entry["contextLength"] = v
	} else {
		delete(entry, "contextLength")
	}
}

func setBoolField(entry map[string]any, key string, v *bool) {
	if v != nil {
		entry[key] = *v
	} else {
		delete(entry, key)
	}
}

func intOf(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func firstInt(vs ...*int64) int64 {
	for _, v := range vs {
		if v != nil && *v > 0 {
			return *v
		}
	}
	return 0
}

// ---------- Kimi Code：config.toml ----------

func buildKimi(home string, p Params) (map[string]string, error) {
	path := filepath.Join(kimiHome(home), "config.toml")
	src := readText(path)
	names, def := agentModels(p)
	src = tomlSetTop(src, "default_model", tomlQuote(providerID+"/"+def))
	src = tomlSetSection(src, "providers."+providerID, [][2]string{
		{"type", tomlQuote("openai")},
		{"base_url", tomlQuote(p.openaiBase())},
		{"api_key", tomlQuote(p.Key)},
	})
	src = tomlRemoveSections(src, func(h string) bool {
		return strings.HasPrefix(h, `models."`+providerID+`/`)
	})
	for _, m := range names {
		src = tomlSetSection(src, `models."`+providerID+`/`+m+`"`, [][2]string{
			{"provider", tomlQuote(providerID)},
			{"model", tomlQuote(m)},
			{"display_name", tomlQuote(m)},
			// 上下文上限取站点「上下文」列；站点没报才退回 20 万（Kimi Code 要一个正数）。
			{"max_context_size", strconv.FormatInt(kimiContext(p, m), 10)},
			{"capabilities", `["tool_use"]`},
		})
	}
	return map[string]string{path: src}, nil
}

// kimiContext：Kimi Code 的 max_context_size。站点报上下文就用真值，没报退回 200000。
func kimiContext(p Params, model string) int64 {
	if item, ok := p.itemOf(model); ok {
		if v := intOf(item.ContextLength); v > 0 {
			return v
		}
	}
	return 200000
}

// ---------- Grok Build：config.toml ----------

func buildGrok(home string, p Params) (map[string]string, error) {
	path := filepath.Join(grokHome(home), "config.toml")
	src := readText(path)
	names, def := agentModels(p)
	src = tomlSetSection(src, "models", [][2]string{
		{"default", tomlQuote(providerID + "/" + def)},
	})
	src = tomlRemoveSections(src, func(h string) bool {
		return strings.HasPrefix(h, `model."`+providerID+`/`)
	})
	for _, m := range names {
		src = tomlSetSection(src, `model."`+providerID+`/`+m+`"`, [][2]string{
			{"model", tomlQuote(m)},
			{"base_url", tomlQuote(p.openaiBase())},
			{"name", tomlQuote(m)},
			{"api_key", tomlQuote(p.Key)},
			{"api_backend", tomlQuote("chat_completions")},
			// 上下文上限取站点「上下文」列；站点没报才退回 20 万。
			{"context_window", strconv.FormatInt(kimiContext(p, m), 10)},
		})
	}
	return map[string]string{path: src}, nil
}

// ---------- Antigravity CLI：settings.json + 连接文件 ----------

func buildAntigravity(home string, p Params) (map[string]string, error) {
	dir := antigravityDir(home)
	settingsPath := filepath.Join(dir, "settings.json")
	root, err := parseJSONObject(readText(settingsPath), "Antigravity settings.json")
	if err != nil {
		return nil, err
	}
	// 全量清单：一个模型一条自定义模型条目
	custom := ensureObject(root, "customModelsConfig", "customModels")
	for key := range custom {
		if strings.HasPrefix(key, "Aster · ") {
			delete(custom, key)
		}
	}
	names, def := agentModels(p)
	for _, name := range names {
		custom["Aster · "+name] = map[string]any{"modelName": name}
	}
	root["modelProvider"] = "gemini"

	connectionPath := filepath.Join(dir, antigravityConnectionFile)
	connection := map[string]any{
		"provider": providerID,
		"baseUrl":  p.Base,
		"apiKey":   p.Key,
		"model":    def,
	}
	return map[string]string{
		settingsPath:   renderJSON(root),
		connectionPath: renderJSON(connection),
	}, nil
}

// ============================================================================
// 第二批客户端写入器（2026-10-16）：DSH / Pi / MiniMax Code / Qwen Code / CodeBuddy
// 说明见 clients.go 头顶那段；字段口径与门户「完整配置导出」逐项一致。
// ============================================================================

// providerDisplay：写进各客户端「显示名」那一格的值（门户导出同款）。
const providerDisplay = "Aster 中转站"

// apiKeyRef：写进 apiKeyEnv / envKey 的凭据引用名。门户导出（DSH_KEY_ENV）、
// DSH 设置页（deriveKeyRef('aster') = ASTER_API_KEY）与 Qwen 的 envKey 三处同名——
// 谁先写都互通，别改成别的拼法。
const apiKeyRef = "ASTER_API_KEY"

// codebuddyVendor：CodeBuddy 条目的 vendor 值（门户导出同款），也当托管标记用。
const codebuddyVendor = "Aster"

// piAiEfforts：pi-ai 的档位枚举——DSH 的 reasoningEfforts 键/值必须落在其中。
var piAiEfforts = map[string]bool{
	"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// ---------- YAML 节点小工具（补丁数组 / 嵌套映射用） ----------

// yamlMapNode：取映射里某个键的值节点（不建、不存在返回 nil）。
func yamlMapNode(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// yamlMapSetNode：按键替换/新增一个值节点。
func yamlMapSetNode(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
}

// yamlMapRemove：删掉一个键（不存在则无事）。
func yamlMapRemove(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

// yamlMapSetInt：数字必须写成 !!int——带引号的 "1" DSH 会当字符串、凭据文件的版本校验直接拒。
func yamlMapSetInt(mapping *yaml.Node, key string, v int64) {
	yamlMapSetNode(mapping, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(v, 10)})
}

// rootOf：把 Encode 出来的节点取出根（Encode(映射) 会包一层文档节点）。
func rootOf(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	return n
}

// encodeYAML：Go 值 → YAML 节点（根）。
func encodeYAML(v any) (*yaml.Node, error) {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	return rootOf(&n), nil
}

// renderYAML：节点 → 文本（两格缩进，与 DSH 自己写的形状一致）。
func renderYAML(doc *yaml.Node) (string, error) {
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return "", err
	}
	enc.Close()
	return sb.String(), nil
}

// yamlSeqEntryByID：在补丁数组里找 id == want 的条目映射。
func yamlSeqEntryByID(seq *yaml.Node, want string) *yaml.Node {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	for _, item := range seq.Content {
		if item.Kind == yaml.MappingNode && yamlMapGet(item, "id") == want {
			return item
		}
	}
	return nil
}

// envWithKV：.env 里设置一个 NAME=VALUE（删同名旧行，其余行与顺序原样保留）。
func envWithKV(existing, name, value string) string {
	var kept []string
	for _, line := range strings.Split(existing, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), name+"=") {
			continue
		}
		kept = append(kept, line)
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	kept = append(kept, name+"="+value)
	return strings.Join(kept, "\n") + "\n"
}

// ---------- DeepSeek Harness（DSH）：profile patch + 凭据存储 ----------
//
// 0.2.x 起的 DSH 把用户层配置放在每个 profile 的 cordis.patch.yml（顶层 YAML 数组，
// 按 id 命中条目后整段替换该条的 config），密钥走官方凭据存储 $DSH_HOME/.credentials.yaml
// （version: 1 + refs）——「设置 → 模型」写的就是这两处。本工具照抄同一形状，与门户
// 「完整配置导出」的 dsh 档字段口径逐项一致；2026-10-16 在 0.2.1-alpha.4 隔离 DSH_HOME
// 实测：补丁零告警落地、真对话 200（含 reasoningEffort=max）。

const (
	// dshPatchEntryID：DSH 里承载 provider 路由的官方插件条目。
	dshPatchEntryID = "llm-pi-ai"
	// dshPatchEntryName：条目 name 字段（命中按 id，name 只做可读性）。
	dshPatchEntryName = "@deepseek-ai/dsh-llm-pi-ai"
	// dshDefaultModelEntryID：新会话默认模型那条的 id/name。
	dshDefaultModelEntryID   = "agent-default-model"
	dshDefaultModelEntryName = "@deepseek-ai/dsh-agent-default-model"
)

func buildDsh(home string, p Params) (map[string]string, error) {
	plan := map[string]string{}
	for _, path := range dshProfilePatches(home) {
		src, err := dshPatchWithProvider(readText(path), p)
		if err != nil {
			return nil, fmt.Errorf("%s：%w", path, err)
		}
		plan[path] = src
	}
	credPath := dshCredentialsPath(home)
	cred, err := dshCredentialsWithKey(readText(credPath), p.Key)
	if err != nil {
		return nil, fmt.Errorf("%s：%w", credPath, err)
	}
	plan[credPath] = cred
	return plan, nil
}

// dshPatchManaged：profile patch 里已有指向本站的 provider（「已托管」判据用）。
func dshPatchManaged(existing string) bool {
	if strings.TrimSpace(existing) == "" {
		return false
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(existing), &doc); err != nil || len(doc.Content) == 0 {
		return false
	}
	entry := yamlSeqEntryByID(doc.Content[0], dshPatchEntryID)
	if entry == nil {
		return false
	}
	providers := yamlMapNode(yamlMapNode(entry, "config"), "providers")
	return providers != nil && yamlMapNode(providers, providerID) != nil
}

// dshPatchWithProvider：把 providers.aster 并进（或建出）llm-pi-ai 条目。
// 只动这一个键：同文件里别的条目、别的 provider、注释都原样保留（patch 的
// 「按 id 整段替换 config」语义决定不能另起一条同名条目）。
func dshPatchWithProvider(existing string, p Params) (string, error) {
	var doc yaml.Node
	if strings.TrimSpace(existing) == "" {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.SequenceNode}}}
	} else if err := yaml.Unmarshal([]byte(existing), &doc); err != nil {
		return "", fmt.Errorf("profile patch 不是合法 YAML：%w", err)
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.SequenceNode}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.SequenceNode {
		return "", fmt.Errorf("profile patch 的根必须是条目数组")
	}

	entry := yamlSeqEntryByID(root, dshPatchEntryID)
	if entry == nil {
		node, err := encodeYAML(map[string]any{"id": dshPatchEntryID, "name": dshPatchEntryName})
		if err != nil {
			return "", err
		}
		entry = node
		root.Content = append(root.Content, entry)
	}
	providers := yamlMapMap(yamlMapMap(entry, "config"), "providers")
	profile, err := encodeYAML(dshProviderProfile(p))
	if err != nil {
		return "", err
	}
	yamlMapSetNode(providers, providerID, profile)

	// 默认模型：条目不存在时补一条指向 aster 的；已存在且指向别家时**不抢**——
	// 那是用户自己选的默认（门户导出的口径也是「agent-default-model 用户自选」）。
	def := p.Model
	if def == "" {
		if all := orderedModels(p); len(all) > 0 {
			def = all[0]
		}
	}
	defEntry := yamlSeqEntryByID(root, dshDefaultModelEntryID)
	if defEntry == nil {
		node, err := encodeYAML(map[string]any{
			"id":   dshDefaultModelEntryID,
			"name": dshDefaultModelEntryName,
			"config": map[string]any{
				"provider": providerID,
				"model":    def,
			},
		})
		if err != nil {
			return "", err
		}
		root.Content = append(root.Content, node)
	} else if cur := yamlMapGet(yamlMapMap(defEntry, "config"), "provider"); cur == "" || cur == providerID {
		cfg := yamlMapMap(defEntry, "config")
		yamlMapSetScalar(cfg, "provider", providerID)
		if def != "" && yamlMapGet(cfg, "model") != def {
			yamlMapSetScalar(cfg, "model", def)
			// 模型换了：旧档位拼写对新模型未必成立（与参考项目同款处置）。
			yamlMapRemove(cfg, "reasoningEffort")
		}
	}
	return renderYAML(&doc)
}

// dshProviderProfile：providers.aster 的整段（重建式，同目录下别的 provider 不动）。
// 字段与门户导出逐项一致：api / baseURL / apiKeyEnv / compat + 每模型的
// contextWindow / maxTokens / input / reasoningEfforts；媒体行用占位上下文。
func dshProviderProfile(p Params) map[string]any {
	models := []any{}
	for _, m := range orderedModels(p) {
		entry := map[string]any{"id": m}
		if item, ok := p.itemOf(m); ok {
			if item.IsMedia() {
				entry["contextWindow"] = MEDIA_CONTEXT_FALLBACK
			} else if v := intOf(item.ContextLength); v > 0 {
				entry["contextWindow"] = v
			}
			if v := item.OutCap(); v > 0 {
				entry["maxTokens"] = v
			}
			if item.SupportsImages != nil && *item.SupportsImages {
				entry["input"] = []any{"text", "image"}
			}
			if efforts := dshEfforts(item.ReasoningEfforts); len(efforts) > 0 {
				entry["reasoningEfforts"] = efforts
			}
		}
		models = append(models, entry)
	}
	return map[string]any{
		"displayName": providerDisplay,
		"api":         "openai-completions",
		"baseURL":     p.openaiBase(),
		"apiKeyEnv":   apiKeyRef,
		"compat":      map[string]any{"supportsDeveloperRole": false},
		"models":      models,
	}
}

// dshEfforts：清单档位 → reasoningEfforts 映射（键=值=档位名）。「off」不写：本站引擎
// 收到 reasoning_effort:"off" 会 500（2026-09-30 qoder 实测），「不选档位」本来就等于
// 不传该参数；站外拼法也不放行（键必须落在 pi-ai 的枚举里）。
func dshEfforts(efforts []string) map[string]any {
	out := map[string]any{}
	for _, e := range efforts {
		e = strings.TrimSpace(e)
		if e == "" || e == "off" || !piAiEfforts[e] {
			continue
		}
		out[e] = e
	}
	return out
}

// dshCredentialsWithKey：写 $DSH_HOME/.credentials.yaml 的 refs.ASTER_API_KEY。
// 形状照 DSH 自己的存储（credentials-local）：version: 1 + refs（POSIX 标识符 → 非空字符串）。
// 预发布期的扁平布局（没有 version 键、顶层全是名字→值）照 DSH 的迁移方式纳进 refs 下；
// 别的 refs / records 条目原样保留。
func dshCredentialsWithKey(existing, key string) (string, error) {
	var doc yaml.Node
	if strings.TrimSpace(existing) == "" {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	} else if err := yaml.Unmarshal([]byte(existing), &doc); err != nil {
		return "", fmt.Errorf("凭据文件不是合法 YAML：%w", err)
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return "", fmt.Errorf("凭据文件的根必须是映射")
	}
	switch ver := yamlMapGet(root, "version"); ver {
	case "":
		if len(root.Content) > 0 {
			// 扁平布局：现有条目整体挪到 refs 下，再补 version。
			refs := &yaml.Node{Kind: yaml.MappingNode, Content: root.Content}
			root.Content = nil
			yamlMapSetInt(root, "version", 1)
			root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "refs"}, refs)
		} else {
			yamlMapSetInt(root, "version", 1)
		}
	case "1":
		// 现行布局，直接用。
	default:
		return "", fmt.Errorf("凭据文件声明 version %s，本工具只会写 version: 1（请先用 DSH 打开一次或手工迁移）", ver)
	}
	yamlMapSetScalar(yamlMapMap(root, "refs"), apiKeyRef, key)
	return renderYAML(&doc)
}

// ---------- Pi：~/.pi/agent/models.json（+ settings.json 默认模型） ----------
//
// 照 cc-switch 的 pi_config：provider 条目在 models.json.providers.<键>（name / baseUrl /
// api / apiKey / models[]），Pi 自己占 settings.json 的账号与默认；本工具像对
// Kimi/Grok 一样把默认模型也指过来（defaultProvider/defaultModel 两个键）。
// 媒体行不进清单（编码 agent 的取舍，与 Kimi/Grok 同）。

func buildPi(home string, p Params) (map[string]string, error) {
	dir := piHome(home)
	modelsPath := filepath.Join(dir, "models.json")
	settingsPath := filepath.Join(dir, "settings.json")

	root, err := parseJSONObject(readText(modelsPath), "Pi models.json")
	if err != nil {
		return nil, err
	}
	providers := ensureObject(root, "providers")
	provider, _ := providers[providerID].(map[string]any)
	if provider == nil {
		provider = map[string]any{}
	}
	provider["name"] = providerDisplay
	provider["baseUrl"] = p.openaiBase()
	provider["api"] = "openai-completions"
	provider["apiKey"] = p.Key
	names, def := agentModels(p)
	entries := []any{}
	for _, m := range names {
		entry := map[string]any{"id": m, "name": m}
		if item, ok := p.itemOf(m); ok {
			if item.SupportsReasoning != nil {
				entry["reasoning"] = *item.SupportsReasoning
			}
			if item.SupportsImages != nil && *item.SupportsImages {
				entry["input"] = []any{"text", "image"}
			}
			if v := intOf(item.ContextLength); v > 0 {
				entry["contextWindow"] = v
			}
			if v := item.OutCap(); v > 0 {
				entry["maxTokens"] = v
			}
		}
		entries = append(entries, entry)
	}
	provider["models"] = entries
	providers[providerID] = provider

	settings, err := parseJSONObject(readText(settingsPath), "Pi settings.json")
	if err != nil {
		return nil, err
	}
	settings["defaultProvider"] = providerID
	settings["defaultModel"] = def

	return map[string]string{
		modelsPath:   renderJSON(root),
		settingsPath: renderJSON(settings),
	}, nil
}

// ---------- MiniMax Code / mcode：~/.minimax/config.yaml ----------
//
// 形状照 cc-switch 的 mcode_config：custom_provider.<键>（name / kind: custom / enabled /
// api / options{baseURL,apiKey} / models{<id>: {name, reasoning, modalities, limit}}）。
// kind=custom 是它的托管标记：MCode 自己的账号 provider 是别的 kind、不归工具碰。
// 媒体行不进清单（编码 CLI 取舍）。

func minimaxCodeManaged(existing string) bool {
	if strings.TrimSpace(existing) == "" {
		return false
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(existing), &doc); err != nil || len(doc.Content) == 0 {
		return false
	}
	return yamlMapNode(yamlMapNode(doc.Content[0], "custom_provider"), providerID) != nil
}

func buildMinimaxCode(home string, p Params) (map[string]string, error) {
	path := filepath.Join(minimaxCodeHome(home), "config.yaml")
	existing := readText(path)
	var doc yaml.Node
	if strings.TrimSpace(existing) == "" {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	} else if err := yaml.Unmarshal([]byte(existing), &doc); err != nil {
		return nil, fmt.Errorf("MiniMax Code config.yaml 不是合法 YAML：%w", err)
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("MiniMax Code config.yaml 的根必须是映射")
	}

	models := map[string]any{}
	names, _ := agentModels(p)
	for _, m := range names {
		entry := map[string]any{"name": m}
		if item, ok := p.itemOf(m); ok {
			if item.SupportsReasoning != nil && *item.SupportsReasoning {
				entry["reasoning"] = true
			}
			inputs := []any{"text"}
			if item.SupportsImages != nil && *item.SupportsImages {
				inputs = append(inputs, "image")
			}
			entry["modalities"] = map[string]any{"input": inputs, "output": []any{"text"}}
			limit := map[string]any{}
			if v := intOf(item.ContextLength); v > 0 {
				limit["context"] = v
			}
			if v := item.OutCap(); v > 0 {
				limit["output"] = v
			}
			if len(limit) > 0 {
				entry["limit"] = limit
			}
		}
		models[m] = entry
	}
	provider, err := encodeYAML(map[string]any{
		"name":    providerDisplay,
		"kind":    "custom",
		"enabled": true,
		"api":     "openai-completions",
		"options": map[string]any{"baseURL": p.openaiBase(), "apiKey": p.Key},
		"models":  models,
	})
	if err != nil {
		return nil, err
	}
	yamlMapSetNode(yamlMapMap(root, "custom_provider"), providerID, provider)
	rendered, err := renderYAML(&doc)
	if err != nil {
		return nil, err
	}
	return map[string]string{path: rendered}, nil
}

// ---------- Qwen Code：~/.qwen/settings.json + .env ----------
//
// 形状照门户导出的 qwen 档（出处 qwen-code 官方文档）：modelProviders.openai[] 一条模型一个
// 条目（id / name / baseUrl=根/v1 / envKey / wireApi），security.auth.selectedType=openai，
// model.name 选默认。**密钥不进 settings.json**——文件里只引用环境变量名，值写同目录 .env。
// 本站条目靠 envKey == ASTER_API_KEY 认领；别人的条目原样保留。媒体行也列（名字带后缀，
// 与门户导出同口径——它们走 chat 能出片）。

func buildQwen(home string, p Params) (map[string]string, error) {
	dir := qwenHome(home)
	settingsPath := filepath.Join(dir, "settings.json")
	envFile := filepath.Join(dir, ".env")

	root, err := parseJSONObject(readText(settingsPath), "Qwen Code settings.json")
	if err != nil {
		return nil, err
	}
	ensureObject(root, "security", "auth")["selectedType"] = "openai"
	_, def := agentModels(p)
	ensureObject(root, "model")["name"] = def
	providers := ensureObject(root, "modelProviders")
	var kept []any
	if list, ok := providers["openai"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok && m["envKey"] == apiKeyRef {
				continue
			}
			kept = append(kept, item)
		}
	}
	for _, id := range orderedModels(p) {
		name := id
		if item, ok := p.itemOf(id); ok && item.IsMedia() {
			name = id + "（生成类 · 走 chat 出片）"
		}
		kept = append(kept, map[string]any{
			"id": id, "name": name, "baseUrl": p.openaiBase(), "envKey": apiKeyRef, "wireApi": "chat-completions",
		})
	}
	providers["openai"] = kept

	return map[string]string{
		settingsPath: renderJSON(root),
		envFile:      envWithKV(readText(envFile), apiKeyRef, p.Key),
	}, nil
}

// ---------- CodeBuddy：~/.codebuddy/models.json ----------
//
// 形状照门户导出的 codebuddy 档（出处 codebuddy.cn 官方文档）：models[] 每条
// {id, name, vendor, apiKey, url, maxInputTokens, maxOutputTokens, supportsToolCall,
//  supportsImages, supportsReasoning}；**url 必须写接口完整路径、以 /chat/completions
// 结尾**。文件形态与 WorkBuddy 同族（对象含 models 数组 / 纯数组两种都收）；本站条目靠
// vendor == "Aster" 认领，别人的条目原样保留。媒体行也列（门户导出同口径）。

func buildCodeBuddy(home string, p Params) (map[string]string, error) {
	path := filepath.Join(codebuddyHome(home), "models.json")
	existing := strings.TrimSpace(readText(path))
	var root any = map[string]any{}
	if existing != "" {
		if err := json.Unmarshal([]byte(existing), &root); err != nil {
			return nil, fmt.Errorf("CodeBuddy models.json 不是合法 JSON：%w", err)
		}
	}
	isArray := false
	if _, ok := root.([]any); ok {
		isArray = true
	}

	var entries []any
	for _, m := range workbuddyModels(root) {
		if m["vendor"] != codebuddyVendor {
			entries = append(entries, m)
		}
	}
	for _, id := range orderedModels(p) {
		entry := map[string]any{
			"id": id, "name": id, "vendor": codebuddyVendor, "apiKey": p.Key,
			"url": p.openaiBase() + "/chat/completions",
		}
		if item, ok := p.itemOf(id); ok {
			if item.IsMedia() {
				entry["name"] = id + "（生成类 · 走 chat 出片）"
				entry["maxInputTokens"] = MEDIA_CONTEXT_FALLBACK
				entry["supportsToolCall"] = false
				entry["supportsImages"] = false
			} else {
				if v := firstInt(item.MaxAllowedSize, item.ContextLength); v > 0 {
					entry["maxInputTokens"] = v
				}
				if v := item.OutCap(); v > 0 {
					entry["maxOutputTokens"] = v
				}
				setBoolField(entry, "supportsToolCall", item.SupportsToolCall)
				setBoolField(entry, "supportsImages", item.SupportsImages)
				if item.SupportsReasoning != nil && *item.SupportsReasoning {
					entry["supportsReasoning"] = true
				}
			}
		}
		entries = append(entries, entry)
	}

	if isArray {
		root = entries
	} else {
		obj, _ := root.(map[string]any)
		if obj == nil {
			obj = map[string]any{}
		}
		if len(entries) == 0 {
			delete(obj, "models")
		} else {
			obj["models"] = entries
		}
		root = obj
	}
	return map[string]string{path: renderJSON(root)}, nil
}

// ---------- Chatbox：%APPDATA%\xyz.chatboxapp.app|ce\config.json ----------
//
// Chatbox 是 electron-store 的纯 JSON（官方没有「必须走 UI」的限制，UI 只是同一个 setter），
// 但**必须在 App 完全退出时写**——它在启动时把 config.json 读进内存、之后只认内存，运行中
// 手改会被整个 settings 覆盖回去（页面上的通用提醒同样适用）。形状照官方 settings-schema：
// settings.customProviders[]（{id,name,type,isCustom:true}）+ settings.providers[id]
// （{apiHost,apiPath,apiKey,models:[{modelId,nickname,capabilities,contextWindow,maxOutput}]}），
// 两边 id 必须一致；OpenAI 兼容的默认 host 是 https://api.openai.com/v1 + path=/chat/completions
// ⇒ 我们写 根/v1 + /chat/completions。

const (
	// chatboxProviderID：固定 id（Chatbox 自己建的是 custom-provider-<uuid>）——固定下来
	// 「已托管」判定与重建都用它；customProviders 里的 id 与 providers 的键必须一致。
	chatboxProviderID = "custom-provider-aster"
	// chatboxSettingsVersion：settings.__version。**新建文件必须写对**：不写会被当旧版
	// 跑一遍迁移（migrateLegacyProviderSettings），字段对不上就会被丢掉。
	chatboxSettingsVersion = 6
)

func buildChatbox(home string, p Params) (map[string]string, error) {
	path := filepath.Join(chatboxDataDir(home), "config.json")
	root, err := parseJSONObject(readText(path), "Chatbox config.json")
	if err != nil {
		return nil, err
	}
	settings, _ := root["settings"].(map[string]any)
	if settings == nil {
		settings = map[string]any{}
		root["settings"] = settings
	}
	if len(settings) > 0 && settings["__version"] == nil {
		// 老格式（2023 年前的扁平键）：官方迁移器会在下次启动时把它搬成新形状，
		// 我们这时写进去的新形状会被迁移流程盖掉——不猜，让用户先启动一次。
		return nil, fmt.Errorf("Chatbox 配置是老格式（settings 里没有 __version）：请先启动一次最新版 Chatbox 让它完成迁移，再回来写入")
	}
	if settings["__version"] == nil {
		settings["__version"] = chatboxSettingsVersion
	}

	var kept []any
	if list, ok := settings["customProviders"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok && m["id"] == chatboxProviderID {
				continue
			}
			kept = append(kept, item)
		}
	}
	kept = append(kept, map[string]any{
		"id": chatboxProviderID, "name": providerDisplay, "type": "openai", "isCustom": true,
	})
	settings["customProviders"] = kept

	models := []any{}
	for _, id := range orderedModels(p) {
		entry := map[string]any{"modelId": id}
		if item, ok := p.itemOf(id); ok {
			if item.IsMedia() {
				entry["nickname"] = id + "（生成类 · 走 chat 出片）"
			}
			if v := intOf(item.ContextLength); v > 0 {
				entry["contextWindow"] = v
			}
			if v := item.OutCap(); v > 0 {
				entry["maxOutput"] = v
			}
			caps := []any{}
			if item.SupportsImages != nil && *item.SupportsImages {
				caps = append(caps, "vision")
			}
			if item.SupportsReasoning != nil && *item.SupportsReasoning {
				caps = append(caps, "reasoning")
			}
			if item.SupportsToolCall != nil && *item.SupportsToolCall {
				caps = append(caps, "tool_use")
			}
			if len(caps) > 0 {
				entry["capabilities"] = caps
			}
		}
		models = append(models, entry)
	}
	ensureObject(settings, "providers")[chatboxProviderID] = map[string]any{
		"apiHost":  p.openaiBase(),
		"apiPath":  "/chat/completions",
		"apiKey":   p.Key,
		"models":   models,
		"useProxy": false,
	}
	_, def := agentModels(p)
	settings["defaultChatModel"] = map[string]any{"provider": chatboxProviderID, "model": def}

	return map[string]string{path: renderJSON(root)}, nil
}

// ---------- Trae CLI（TraeCode 1.x）：%APPDATA%\trae_cli\trae_cli.yaml ----------
//
// 官方文档（docs.trae.cn/cli_model.md）：直接编辑全局 trae_cli.yaml，`models:` 数组每条
// `- name: <展示名>` + `open_ai: {base_url, api_key, model}`（base_url 官方就写明用于
// 「代理、中转、私有部署」）。旧条目按 name 前缀认领重建，别人的条目原样保留；媒体行不进
// 清单（编码 CLI 的取舍，与 Kimi/Grok/Pi 同）。注意 CLI 2.0 的 ~/.trae/traecli.toml 不写。

const traeModelPrefix = "Aster · "

func traeManaged(existing string) bool {
	if strings.TrimSpace(existing) == "" {
		return false
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(existing), &doc); err != nil || len(doc.Content) == 0 {
		return false
	}
	seq := yamlMapNode(doc.Content[0], "models")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return false
	}
	for _, item := range seq.Content {
		if strings.HasPrefix(yamlMapGet(item, "name"), traeModelPrefix) {
			return true
		}
	}
	return false
}

func buildTrae(home string, p Params) (map[string]string, error) {
	path := traeConfigPath(home)
	existing := readText(path)
	var doc yaml.Node
	if strings.TrimSpace(existing) == "" {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	} else if err := yaml.Unmarshal([]byte(existing), &doc); err != nil {
		return nil, fmt.Errorf("Trae CLI trae_cli.yaml 不是合法 YAML：%w", err)
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("Trae CLI trae_cli.yaml 的根必须是映射")
	}
	seq := yamlMapSeq(root, "models")
	var kept []*yaml.Node
	for _, item := range seq.Content {
		if strings.HasPrefix(yamlMapGet(item, "name"), traeModelPrefix) {
			continue
		}
		kept = append(kept, item)
	}
	names, _ := agentModels(p)
	for _, m := range names {
		node, err := encodeYAML(map[string]any{
			"name": traeModelPrefix + m,
			"open_ai": map[string]any{
				"base_url": p.openaiBase(),
				"api_key":  p.Key,
				"model":    m,
			},
		})
		if err != nil {
			return nil, err
		}
		kept = append(kept, node)
	}
	seq.Content = kept
	rendered, err := renderYAML(&doc)
	if err != nil {
		return nil, err
	}
	return map[string]string{path: rendered}, nil
}
