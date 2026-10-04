package main

// 客户端注册表：检测判据、配置文件路径、"已托管"标记。
// 判据与路径形状移植自 EasyCLIProxyAPI（src-tauri/src/agents/discovery.rs），
// 托管 provider id 由它的 "cpa-gui" 换成我们自己的 "aster"。

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const providerID = "aster"   // 写进各客户端配置的托管 provider 标识
const providerName = "Aster" // 展示名
const catalogFile = "aster-model-catalog.json"
const claudeDesktopProfileID = "00000000-0000-4000-8000-00000000a57e"
const antigravityConnectionFile = "aster-connection.json"
const antigravityModelLabel = "Aster (aster-onboard)"

// Params 是写配置时的全部输入：中转站根地址、密钥、本客户端选的默认模型、模型目录。
type Params struct {
	Base   string   // 根地址，如 https://api.xiaoshi888.top（不带尾斜杠）
	Key    string   // 朋友的中转站密钥
	Model  string   // 该客户端的默认模型
	Models []string // 中转站 /v1/models 拉回来的全量 id
	// Rates 是 模型 id → 中转站 /v1/models 的 credits 文案（如 "x0.80"）。
	// 认这个字段的客户端（WorkBuddy）拿它当消耗倍率直接显示；没有的条目客户端就不显示。
	Rates map[string]string
}

// rateOf 返回某个模型的中转站倍率文案；没下发的返回空串（调用方据此省略字段）。
func (p Params) rateOf(model string) string {
	return strings.TrimSpace(p.Rates[model])
}

func (p Params) openaiBase() string { return p.Base + "/v1" }

type Client struct {
	ID   string
	Name string
	Kind string // "CLI" 或 "桌面端"
	// Paths 返回要被写入/备份的配置文件（顺序固定）。
	Paths func(home string) []string
	// Detect 返回"本机装了"的证据（存在的目录/文件/可执行命令）。
	Detect func(home string) []string
	// Managed 判断配置里是否已经指向我们的托管 provider。
	Managed func(home string) bool
	// Build 读现有内容、产出 路径→新内容 的写入计划。
	Build func(home string, p Params) (map[string]string, error)
}

func homeDir() string {
	if h := os.Getenv("ASTER_ONBOARD_HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// appSupportDir(home) 由平台文件提供：
// Windows = %LOCALAPPDATA%，macOS = ~/Library/Application Support（见 platform_*.go）。

func envPath(name string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" || os.Getenv("ASTER_ONBOARD_HOME") != "" {
		// 沙箱测试时 home 是假的，环境覆盖一律不作数，避免写进真目录
		if os.Getenv("ASTER_ONBOARD_HOME") != "" {
			return ""
		}
	}
	return v
}

func lookPath(names ...string) []string {
	var out []string
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func exists(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func readJSON(path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var v map[string]any
	if json.Unmarshal(stripJSONC(raw), &v) != nil {
		return nil
	}
	return v
}

// ---------- 各客户端的 home / 路径 ----------

func codexHome(home string) string {
	if v := envPath("CODEX_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".codex")
}

func opencodeConfigPath(home string) string {
	if v := envPath("OPENCODE_CONFIG"); v != "" {
		return v
	}
	dir := filepath.Join(xdgConfig(home), "opencode")
	jsonPath := filepath.Join(dir, "opencode.json")
	if _, err := os.Stat(jsonPath); err == nil {
		return jsonPath
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.jsonc")); err == nil {
		return filepath.Join(dir, "opencode.jsonc")
	}
	return jsonPath
}

func xdgConfig(home string) string {
	if v := envPath("XDG_CONFIG_HOME"); v != "" && filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(home, ".config")
}

func kimiHome(home string) string {
	if v := envPath("KIMI_CODE_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".kimi-code")
}

func grokHome(home string) string {
	if v := envPath("GROK_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".grok")
}

func hermesConfigPath(home string) string {
	if v := envPath("HERMES_HOME"); v != "" {
		return filepath.Join(v, "config.yaml")
	}
	return filepath.Join(appSupportDir(home), "hermes", "config.yaml")
}

// workbuddyHome 解析 WorkBuddy 桌面端的数据目录，与 App 侧 resolveConfigDir 同源：
//  1. WORKBUDDY_CONFIG_DIR（其次 CODEBUDDY_CONFIG_DIR）——启动器注入的，优先；
//  2. WORKBUDDY_DATA_FOLDER_NAME（专享版/私有化发行版用，如 .aimea）→ <home>/<名字>；
//  3. 默认 <home>/.workbuddy（现行桌面端；models.json 就在这个目录下）。
//
// 仅当 .workbuddy 不存在、而海外版的 .workbuddy-ai 在时才退回后者
// （App 里 workbuddy-ai 是"无积分体系的发行版"，不是现行客户端的数据目录）。
func workbuddyHome(home string) string {
	if v := envPath("WORKBUDDY_CONFIG_DIR"); v != "" {
		return v
	}
	if v := envPath("CODEBUDDY_CONFIG_DIR"); v != "" {
		return v
	}
	if v := envPath("WORKBUDDY_DATA_FOLDER_NAME"); v != "" {
		return filepath.Join(home, v)
	}
	dir := filepath.Join(home, ".workbuddy")
	if _, err := os.Stat(dir); err != nil {
		if overseas := filepath.Join(home, ".workbuddy-ai"); len(exists(overseas)) > 0 {
			return overseas
		}
	}
	return dir
}

// claudeDesktopPaths 返回四个文件：常规配置、3p 配置、托管 profile、配置索引。
func claudeDesktopPaths(home string) []string {
	base := appSupportDir(home)
	normal := claudeDataDir(base, false)
	threep := claudeDataDir(base, true)
	library := filepath.Join(threep, "configLibrary")
	return []string{
		filepath.Join(normal, "claude_desktop_config.json"),
		filepath.Join(threep, "claude_desktop_config.json"),
		filepath.Join(library, claudeDesktopProfileID+".json"),
		filepath.Join(library, "_meta.json"),
	}
}

// claudeDataDir 在应用数据目录下找 Claude / Claude-3p（目录名带版本后缀时取第一个）。
func claudeDataDir(base string, threep bool) string {
	exact := "Claude"
	if threep {
		exact = "Claude-3p"
	}
	if st, err := os.Stat(filepath.Join(base, exact)); err == nil && st.IsDir() {
		return filepath.Join(base, exact)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return filepath.Join(base, exact)
	}
	var candidates []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		if strings.HasPrefix(lower, "claude") && strings.Contains(lower, "-3p") == threep {
			candidates = append(candidates, filepath.Join(base, e.Name()))
		}
	}
	sort.Strings(candidates)
	if len(candidates) > 0 {
		return candidates[0]
	}
	return filepath.Join(base, exact)
}

func antigravityDir(home string) string { return filepath.Join(home, ".gemini", "antigravity-cli") }

// ---------- 托管标记 ----------

func managedByBaseOrProvider(path string, pointers ...string) bool {
	root := readJSON(path)
	if root == nil {
		return false
	}
	for _, ptr := range pointers {
		if jsonPointer(root, ptr) != nil {
			return true
		}
	}
	return false
}

func jsonPointer(root any, ptr string) any {
	cur := root
	for _, part := range strings.Split(strings.Trim(ptr, "/"), "/") {
		if part == "" {
			continue
		}
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[part]
			if !ok {
				return nil
			}
			cur = v
		default:
			return nil
		}
	}
	return cur
}

func tomlHasSection(src, header string) bool {
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.TrimSuffix(strings.TrimPrefix(t, "["), "]") == header {
			return true
		}
	}
	return false
}

// ---------- 注册表 ----------

func allClients() []Client {
	return []Client{
		{
			ID: "claude-code", Name: "Claude Code", Kind: "CLI",
			Paths: func(home string) []string {
				settings := filepath.Join(home, ".claude", "settings.json")
				legacy := filepath.Join(home, ".claude", "claude.json")
				if _, err := os.Stat(settings); err != nil {
					if _, err2 := os.Stat(legacy); err2 == nil {
						return []string{legacy}
					}
				}
				return []string{settings}
			},
			Detect: func(home string) []string {
				return append(exists(filepath.Join(home, ".claude")), lookPath("claude")...)
			},
			Managed: func(home string) bool {
				root := readJSON(claudeCodeSettingsPath(home))
				env, _ := root["env"].(map[string]any)
				if env == nil {
					return false
				}
				u, _ := env["ANTHROPIC_BASE_URL"].(string)
				return u != "" && u != "https://api.anthropic.com"
			},
			Build: buildClaudeCode,
		},
		{
			ID: "claude-desktop", Name: "Claude Desktop", Kind: "桌面端",
			Paths: claudeDesktopPaths,
			Detect: func(home string) []string {
				base := appSupportDir(home)
				ev := exists(claudeDataDir(base, false), claudeDataDir(base, true))
				return append(ev, bundleEvidence("claude-desktop")...)
			},
			Managed: func(home string) bool {
				paths := claudeDesktopPaths(home)
				meta := readJSON(paths[3])
				if meta != nil {
					if id, _ := meta["appliedId"].(string); id == claudeDesktopProfileID {
						return true
					}
				}
				profile := readJSON(paths[2])
				return profile != nil && profile["inferenceGatewayBaseUrl"] != nil
			},
			Build: buildClaudeDesktop,
		},
		{
			ID: "codex", Name: "Codex", Kind: "CLI",
			Paths: func(home string) []string {
				d := codexHome(home)
				return []string{filepath.Join(d, "config.toml"), filepath.Join(d, "auth.json"), filepath.Join(d, catalogFile)}
			},
			Detect: func(home string) []string {
				return append(exists(codexHome(home)), lookPath("codex")...)
			},
			Managed: func(home string) bool {
				src, err := os.ReadFile(filepath.Join(codexHome(home), "config.toml"))
				if err != nil {
					return false
				}
				return tomlTopValue(string(src), "model_provider") == `"`+providerID+`"`
			},
			Build: buildCodex,
		},
		{
			ID: "opencode", Name: "OpenCode", Kind: "CLI",
			Paths: func(home string) []string { return []string{opencodeConfigPath(home)} },
			Detect: func(home string) []string {
				return append(exists(filepath.Dir(opencodeConfigPath(home))), lookPath("opencode")...)
			},
			Managed: func(home string) bool {
				return managedByBaseOrProvider(opencodeConfigPath(home), "provider/"+providerID)
			},
			Build: buildOpenCode,
		},
		{
			ID: "openclaw", Name: "OpenClaw", Kind: "CLI",
			Paths: func(home string) []string { return []string{filepath.Join(home, ".openclaw", "openclaw.json")} },
			Detect: func(home string) []string {
				return append(exists(filepath.Join(home, ".openclaw")), lookPath("openclaw")...)
			},
			Managed: func(home string) bool {
				return managedByBaseOrProvider(filepath.Join(home, ".openclaw", "openclaw.json"), "models/providers/"+providerID)
			},
			Build: buildOpenClaw,
		},
		{
			ID: "hermes", Name: "Hermes Agent", Kind: "CLI",
			Paths: func(home string) []string { return []string{hermesConfigPath(home)} },
			Detect: func(home string) []string {
				return append(exists(hermesConfigPath(home)), lookPath("hermes")...)
			},
			Managed: func(home string) bool {
				raw, err := os.ReadFile(hermesConfigPath(home))
				if err != nil {
					return false
				}
				return strings.Contains(string(raw), "name: "+providerID)
			},
			Build: buildHermes,
		},
		{
			ID: "zcode", Name: "ZCode", Kind: "桌面端",
			Paths: func(home string) []string {
				return []string{filepath.Join(home, ".zcode", "v2", "provider_config.json")}
			},
			Detect: func(home string) []string {
				return append(exists(filepath.Join(home, ".zcode")), append(lookPath("zcode"), bundleEvidence("zcode")...)...)
			},
			Managed: func(home string) bool {
				root := readJSON(filepath.Join(home, ".zcode", "v2", "provider_config.json"))
				rules, _ := jsonPointer(root, "config/providerConfigRules/providerRules").([]any)
				for _, r := range rules {
					m, _ := r.(map[string]any)
					if m != nil && m["providerId"] == providerID {
						return true
					}
				}
				return false
			},
			Build: buildZCode,
		},
		{
			ID: "workbuddy", Name: "WorkBuddy / WorkBuddy AI", Kind: "桌面端",
			Paths: func(home string) []string { return []string{filepath.Join(workbuddyHome(home), "models.json")} },
			Detect: func(home string) []string {
				ev := append(exists(workbuddyHome(home)), exists(filepath.Join(appSupportDir(home), "WorkBuddy"))...)
				return append(ev, bundleEvidence("workbuddy")...)
			},
			Managed: func(home string) bool {
				raw, err := os.ReadFile(filepath.Join(workbuddyHome(home), "models.json"))
				if err != nil {
					return false
				}
				var v any
				if json.Unmarshal(raw, &v) != nil {
					return false
				}
				for _, m := range workbuddyModels(v) {
					if m["vendor"] == providerID {
						return true
					}
				}
				return false
			},
			Build: buildWorkBuddy,
		},
		{
			ID: "kimi", Name: "Kimi Code", Kind: "CLI",
			Paths: func(home string) []string { return []string{filepath.Join(kimiHome(home), "config.toml")} },
			Detect: func(home string) []string {
				return append(exists(kimiHome(home)), append(lookPath("kimi"), bundleEvidence("kimi")...)...)
			},
			Managed: func(home string) bool {
				src, err := os.ReadFile(filepath.Join(kimiHome(home), "config.toml"))
				if err != nil {
					return false
				}
				return tomlHasSection(string(src), "providers."+providerID)
			},
			Build: buildKimi,
		},
		{
			ID: "grok", Name: "Grok Build", Kind: "CLI",
			Paths: func(home string) []string { return []string{filepath.Join(grokHome(home), "config.toml")} },
			Detect: func(home string) []string {
				return append(exists(grokHome(home)), append(lookPath("grok"), bundleEvidence("grok")...)...)
			},
			Managed: func(home string) bool {
				src, err := os.ReadFile(filepath.Join(grokHome(home), "config.toml"))
				if err != nil {
					return false
				}
				return strings.Contains(string(src), `"`+providerID+`/`)
			},
			Build: buildGrok,
		},
		{
			ID: "antigravity", Name: "Antigravity CLI", Kind: "CLI",
			Paths: func(home string) []string {
				d := antigravityDir(home)
				return []string{filepath.Join(d, "settings.json"), filepath.Join(d, antigravityConnectionFile)}
			},
			Detect: func(home string) []string {
				ev := exists(antigravityDir(home), filepath.Join(appSupportDir(home), "agy", "bin", "agy.exe"))
				ev = append(ev, lookPath("agy")...)
				return append(ev, bundleEvidence("antigravity")...)
			},
			Managed: func(home string) bool {
				root := readJSON(filepath.Join(antigravityDir(home), antigravityConnectionFile))
				return root != nil && root["provider"] == providerID
			},
			Build: buildAntigravity,
		},
	}
}

func claudeCodeSettingsPath(home string) string {
	return allClients()[0].Paths(home)[0]
}

func workbuddyModels(root any) []map[string]any {
	var list []any
	switch v := root.(type) {
	case []any:
		list = v
	case map[string]any:
		list, _ = v["models"].([]any)
	}
	var out []map[string]any
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
