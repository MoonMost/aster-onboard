package main

// 各客户端配置写入器。形状逐条对拍自 EasyCLIProxyAPI 的 src-tauri/src/agents/configuration.rs，
// 托管 provider id 换成 "aster"、展示名换成 "Aster"；根地址给 Anthropic 系客户端，/v1 给 OpenAI 系。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func catalogEntries(p Params) []any {
	var out []any
	for _, m := range orderedModels(p) {
		out = append(out, map[string]any{"slug": m, "display_name": m})
	}
	return out
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
		models[m] = map[string]any{"name": m}
	}
	provider["models"] = models
	root["model"] = providerID + "/" + p.Model
	return map[string]string{path: withLeadingComments(existing, renderJSON(root))}, nil
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
		list = append(list, map[string]any{"id": m, "name": m})
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
			"api": map[string]any{
				"type":    "anthropic-messages",
				"baseUrl": p.Base,
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
	return map[string]string{path: renderJSON(root)}, nil
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
	// WorkBuddy 的模型选择器读 credits 当消耗倍率显示（列表行右侧、子菜单的「消耗速度」）。
	// 站点没下发倍率的模型就删掉旧值，免得把上一次写的倍率留在配置里。
	if credits := p.rateOf(model); credits != "" {
		entry["credits"] = credits
	} else {
		delete(entry, "credits")
	}
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
			{"max_context_size", "200000"},
			{"capabilities", `["tool_use"]`},
		})
	}
	return map[string]string{path: src}, nil
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
			{"context_window", "200000"},
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
