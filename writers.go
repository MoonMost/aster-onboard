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
	m := p.Model
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
	var inferenceModels []any
	for _, name := range orderedModels(p) {
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

	src = tomlSetTop(src, "model_provider", tomlQuote(providerID))
	src = tomlSetTop(src, "model", tomlQuote(p.Model))
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

	catalog := map[string]any{"models": catalogEntries(p)}
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
func catalogEntries(p Params) []any {
	var out []any
	for _, m := range orderedModels(p) {
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
	models := map[string]any{}
	for _, m := range orderedModels(p) {
		models[m] = opencodeModelEntry(p, m)
	}
	provider["models"] = models
	root["model"] = providerID + "/" + p.Model
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
	ctx, out := intOf(item.ContextLength), intOf(item.MaxOutputTokens)
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
	var list []any
	for _, m := range orderedModels(p) {
		list = append(list, openclawModelEntry(p, m))
	}
	managed["models"] = list

	defaults := ensureObject(root, "agents", "defaults")
	model := ensureObject(defaults, "model")
	model["primary"] = providerID + "/" + p.Model
	catalog := ensureObject(defaults, "models")
	for key := range catalog {
		if strings.HasPrefix(key, providerID+"/") {
			delete(catalog, key)
		}
	}
	for _, m := range orderedModels(p) {
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
	if v := intOf(item.MaxOutputTokens); v > 0 {
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

	providerModels := map[string]any{}
	for _, m := range orderedModels(p) {
		providerModels[m] = map[string]any{}
	}
	var newNode yaml.Node
	if err := newNode.Encode(map[string]any{
		"name":     providerID,
		"base_url": p.openaiBase(),
		"api_key":  p.Key,
		"api_mode": "chat_completions",
		"model":    p.Model,
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
		props["inputFormat"] = map[string]any{
			"supportsText":  true,
			"supportsImage": item.SupportsImages != nil && *item.SupportsImages,
			"supportsVideo": item.SupportsVideo != nil && *item.SupportsVideo,
			"supportsAudio": false,
			"supportsPdf":   false,
		}
		if item.ContextLength != nil && *item.ContextLength > 0 {
			props["contextWindow"] = *item.ContextLength
		} else {
			delete(props, "contextWindow")
		}
		// 档位：引擎声明了几档就写几档；声明了思考但没给清单的按 sync 脚本口径给 ["high"]；
		// 完全不是思考模型给 ["none"]（客户端要一个非空数组，空数组会被校验拒掉）。
		// 不擅自加 "disabled"——「仅思考」的模型关不掉思考，加一档等于对客户端谎报能关。
		levels := item.ReasoningEfforts
		if len(levels) == 0 {
			if item.SupportsReasoning != nil && *item.SupportsReasoning {
				levels = []string{"high"}
			} else {
				levels = []string{"none"}
			}
		}
		specs := ensureObject(c, "optionSpecs")
		specs["reasoningLevel"] = map[string]any{"values": toAnySlice(levels)}
		// 输出上限（官网「输出上限」那一列）：客户端据此知道这个模型最多能吐多少 token，
		// 内置 api 规则会把它映射成 max_completion_tokens。站点没报就不写。
		if v := intOf(item.MaxOutputTokens); v > 0 {
			specs["maxOutputTokens"] = map[string]any{"max": v}
		} else {
			delete(specs, "maxOutputTokens")
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
	if v := intOf(item.MaxOutputTokens); v > 0 {
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
	src = tomlSetTop(src, "default_model", tomlQuote(providerID+"/"+p.Model))
	src = tomlSetSection(src, "providers."+providerID, [][2]string{
		{"type", tomlQuote("openai")},
		{"base_url", tomlQuote(p.openaiBase())},
		{"api_key", tomlQuote(p.Key)},
	})
	src = tomlRemoveSections(src, func(h string) bool {
		return strings.HasPrefix(h, `models."`+providerID+`/`)
	})
	for _, m := range orderedModels(p) {
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
	src = tomlSetSection(src, "models", [][2]string{
		{"default", tomlQuote(providerID + "/" + p.Model)},
	})
	src = tomlRemoveSections(src, func(h string) bool {
		return strings.HasPrefix(h, `model."`+providerID+`/`)
	})
	for _, m := range orderedModels(p) {
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
	for _, name := range orderedModels(p) {
		custom["Aster · "+name] = map[string]any{"modelName": name}
	}
	root["modelProvider"] = "gemini"

	connectionPath := filepath.Join(dir, antigravityConnectionFile)
	connection := map[string]any{
		"provider": providerID,
		"baseUrl":  p.Base,
		"apiKey":   p.Key,
		"model":    p.Model,
	}
	return map[string]string{
		settingsPath:   renderJSON(root),
		connectionPath: renderJSON(connection),
	}, nil
}
