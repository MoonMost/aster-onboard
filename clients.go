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
	// Catalog 是中转站 /v1/models 的完整条目（倍率 + 思考档位 + 媒体能力 + 上下文）。
	// 各客户端的模型覆盖层（ZCode 的 optionSpecs/inputFormat、WorkBuddy 的 reasoning）
	// 全部由它派生：站点声明什么就写什么，引擎没报的字段不编造。
	Catalog []catalogItem
	// Rates 是旧版页面回传的 模型 id → credits 文案；Catalog 里没有的条目才回退读它。
	Rates map[string]string
}

// itemOf 按 id 找目录条目。
func (p Params) itemOf(model string) (catalogItem, bool) {
	for _, m := range p.Catalog {
		if m.ID == model {
			return m, true
		}
	}
	return catalogItem{}, false
}

// rateOf 返回某个模型的旧口径倍率（站点 credits 原文）；没下发的返回空串。
func (p Params) rateOf(model string) string {
	if m, ok := p.itemOf(model); ok && strings.TrimSpace(m.Credits) != "" {
		return strings.TrimSpace(m.Credits)
	}
	return strings.TrimSpace(p.Rates[model])
}

// rateHint 是写进客户端「消耗速度/倍率」那一格的文案——**必须与官网模型清单那一格同值**。
// 口径与门户导出逐字一致（portal.html 的 clientRateHint / workbuddyMediaEntry）：
//
//	聊天行：landing_rate + " 积分/千token"（客户端只取第一个数字画成 "Nx"）
//	媒体行：credits 原文（"≈83 积分/秒"）；只有 landing_rate 时按同一形状拼
//
// landing_rate 缺了退回 credits，都没有再退回旧页面回传的 rates，至少不把这一格弄丢。
func (p Params) rateHint(model string) string {
	item, ok := p.itemOf(model)
	if ok {
		if item.IsMedia() {
			if s := strings.TrimSpace(item.Credits); s != "" {
				return s
			}
			if s := strings.TrimSpace(item.LandingRate); s != "" {
				return "≈" + s + " 积分/秒"
			}
		} else {
			if s := strings.TrimSpace(item.LandingRate); s != "" {
				return s + " 积分/千token"
			}
			if s := strings.TrimSpace(item.Credits); s != "" {
				return s
			}
		}
	}
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

// ---------- 第二批客户端（2026-10-16）：DSH / Pi / MiniMax Code / Qwen Code / CodeBuddy ----------
// 形状出处：门户「完整配置导出」的对应 builder（逐字段口径都写在那里）、cc-switch
// （pi_config/mod.rs、mcode_config.rs）与两家官方文档；DSH 的 profile patch + 凭据存储
// 已在 0.2.1-alpha.4 隔离 DSH_HOME 实机验证（补丁零告警落地、真对话 200）。

// dshHome：DSH_HOME 环境变量可覆盖，缺省 ~/.dsh。
func dshHome(home string) string {
	if v := envPath("DSH_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".dsh")
}

// dshProfilePatches：DSH 每个 profile 的 cordis.patch.yml。只打**已存在**的 profile
// （桌面端 desktop、dsh web 是 web，等等）——没跑过的 profile 不替它建，免得在朋友的
// home 里凭空多出一个 profile 目录；一个都没有时退回 desktop（新装只跑桌面端的环境）。
// profiles/ 下还有 node_modules（DSH 的共享依赖目录）与点开头的内部目录，都不是 profile，
// 必须跳过——真机上就是靠这条才没把 patch 写进 profiles/node_modules/。
func dshProfilePatches(home string) []string {
	root := filepath.Join(dshHome(home), "profiles")
	var out []string
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || name == "node_modules" || strings.HasPrefix(name, ".") {
				continue
			}
			out = append(out, filepath.Join(root, name, "cordis.patch.yml"))
		}
		sort.Strings(out)
	}
	if len(out) == 0 {
		out = []string{filepath.Join(root, "desktop", "cordis.patch.yml")}
	}
	return out
}

func dshCredentialsPath(home string) string {
	return filepath.Join(dshHome(home), ".credentials.yaml")
}

// piHome：Pi 的 agent 目录（照 cc-switch 的探测顺序：PI_CODING_AGENT_DIR → ~/.pi/agent）。
func piHome(home string) string {
	if v := envPath("PI_CODING_AGENT_DIR"); v != "" {
		return v
	}
	return filepath.Join(home, ".pi", "agent")
}

// minimaxCodeHome：MiniMax Code / mcode 的数据目录（官方：MINIMAX_DATA_DIR → MAVIS_DATA_DIR → ~/.minimax）。
func minimaxCodeHome(home string) string {
	if v := envPath("MINIMAX_DATA_DIR"); v != "" {
		return v
	}
	if v := envPath("MAVIS_DATA_DIR"); v != "" {
		return v
	}
	return filepath.Join(home, ".minimax")
}

// qwenHome：Qwen Code 的配置目录（~/.qwen/settings.json + .env）。
func qwenHome(home string) string { return filepath.Join(home, ".qwen") }

// codebuddyHome：CodeBuddy 的用户级配置目录（~/.codebuddy/models.json；项目级
// <repo>/.codebuddy/models.json 会覆盖它——那份不归工具管）。
func codebuddyHome(home string) string { return filepath.Join(home, ".codebuddy") }

// chatboxDataDirs：Chatbox 的两个候选 userData 目录——都在 %APPDATA%（Roaming），
// 不是 LOCALAPPDATA：官网分发版跑在 xyz.chatboxapp.app，自编译 CE 版（v1.17.1 起）
// 跑在 xyz.chatboxapp.ce（官方 issue #2527 的原话）。
func chatboxDataDirs(home string) []string {
	base := configDir(home)
	return []string{filepath.Join(base, "xyz.chatboxapp.app"), filepath.Join(base, "xyz.chatboxapp.ce")}
}

// chatboxDataDir：优先 config.json 已经存在的那个目录；否则选存在的目录（两边都在时选
// 官网版）；一个都没有时也用官网版目录（与其它桌面端同一条：没启动过就没配置可写）。
func chatboxDataDir(home string) string {
	dirs := chatboxDataDirs(home)
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, "config.json")); err == nil {
			return d
		}
	}
	for _, d := range dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return dirs[0]
}

// traeConfigPath：TraeCode CLI（1.x）的用户级配置（官方文档：`traecli config edit` 编辑的就是它）。
// 注意 CLI 2.0 换成了 ~/.trae/traecli.toml（schema 不同），本工具只写 1.x 这份。
func traeConfigPath(home string) string {
	return filepath.Join(configDir(home), "trae_cli", "trae_cli.yaml")
}

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
		{
			ID: "dsh", Name: "DeepSeek Harness（DSH）", Kind: "桌面端",
			Paths: func(home string) []string {
				return append(dshProfilePatches(home), dshCredentialsPath(home))
			},
			Detect: func(home string) []string {
				ev := exists(dshHome(home), filepath.Join(dshHome(home), "profiles"))
				return append(ev, lookPath("dsh")...)
			},
			Managed: func(home string) bool {
				for _, path := range dshProfilePatches(home) {
					if dshPatchManaged(readText(path)) {
						return true
					}
				}
				return false
			},
			Build: buildDsh,
		},
		{
			ID: "pi", Name: "Pi", Kind: "CLI",
			Paths: func(home string) []string {
				d := piHome(home)
				return []string{filepath.Join(d, "models.json"), filepath.Join(d, "settings.json")}
			},
			Detect: func(home string) []string {
				return append(exists(piHome(home)), lookPath("pi")...)
			},
			Managed: func(home string) bool {
				root := readJSON(filepath.Join(piHome(home), "models.json"))
				return jsonPointer(root, "providers/"+providerID) != nil
			},
			Build: buildPi,
		},
		{
			ID: "minimax-code", Name: "MiniMax Code（mcode）", Kind: "CLI",
			Paths: func(home string) []string {
				return []string{filepath.Join(minimaxCodeHome(home), "config.yaml")}
			},
			Detect: func(home string) []string {
				ev := exists(minimaxCodeHome(home), filepath.Join(minimaxCodeHome(home), "config.yaml"))
				return append(ev, lookPath("mcode", "mmx")...)
			},
			Managed: func(home string) bool {
				return minimaxCodeManaged(readText(filepath.Join(minimaxCodeHome(home), "config.yaml")))
			},
			Build: buildMinimaxCode,
		},
		{
			ID: "qwen", Name: "Qwen Code", Kind: "CLI",
			Paths: func(home string) []string {
				return []string{filepath.Join(qwenHome(home), "settings.json"), filepath.Join(qwenHome(home), ".env")}
			},
			Detect: func(home string) []string {
				return append(exists(qwenHome(home)), lookPath("qwen")...)
			},
			Managed: func(home string) bool {
				root := readJSON(filepath.Join(qwenHome(home), "settings.json"))
				list, _ := jsonPointer(root, "modelProviders/openai").([]any)
				for _, item := range list {
					if m, ok := item.(map[string]any); ok && m["envKey"] == apiKeyRef {
						return true
					}
				}
				return false
			},
			Build: buildQwen,
		},
		{
			ID: "codebuddy", Name: "CodeBuddy", Kind: "桌面端",
			Paths: func(home string) []string {
				return []string{filepath.Join(codebuddyHome(home), "models.json")}
			},
			Detect: func(home string) []string {
				return append(exists(codebuddyHome(home)), lookPath("codebuddy")...)
			},
			Managed: func(home string) bool {
				root := readJSON(filepath.Join(codebuddyHome(home), "models.json"))
				for _, m := range workbuddyModels(root) {
					if m["vendor"] == codebuddyVendor {
						return true
					}
				}
				return false
			},
			Build: buildCodeBuddy,
		},
		{
			ID: "chatbox", Name: "Chatbox", Kind: "桌面端",
			Paths: func(home string) []string {
				return []string{filepath.Join(chatboxDataDir(home), "config.json")}
			},
			Detect: func(home string) []string {
				return exists(chatboxDataDirs(home)...)
			},
			Managed: func(home string) bool {
				root := readJSON(filepath.Join(chatboxDataDir(home), "config.json"))
				list, _ := jsonPointer(root, "settings/customProviders").([]any)
				for _, item := range list {
					if m, ok := item.(map[string]any); ok && m["id"] == chatboxProviderID {
						return true
					}
				}
				return false
			},
			Build: buildChatbox,
		},
		{
			ID: "trae", Name: "Trae CLI（TraeCode）", Kind: "CLI",
			Paths: func(home string) []string { return []string{traeConfigPath(home)} },
			Detect: func(home string) []string {
				ev := exists(filepath.Join(configDir(home), "trae_cli"))
				return append(ev, lookPath("traecli")...)
			},
			Managed: func(home string) bool {
				return traeManaged(readText(traeConfigPath(home)))
			},
			Build: buildTrae,
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
