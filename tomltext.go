package main

// 文本级 TOML 手术：只动我们托管的键/段，其余字节原样保留（注释、顺序、别人的段都不碰）。
// 不引 TOML 库做"解析-重排"，因为那会抹掉用户注释——参考项目用 toml_edit 保格式，我们用文本替换达到同样效果。

import (
	"strconv"
	"strings"
)

func tomlQuote(s string) string { return strconv.Quote(s) }

// tomlKey 把段名零件按 TOML 规则加引号：裸标识符原样，其余加引号。
func tomlKey(part string) string {
	if part != "" && !strings.ContainsAny(part, "\"'./ []=") && isBareKey(part) {
		return part
	}
	return tomlQuote(part)
}

func isBareKey(s string) bool {
	for _, r := range s {
		if !(r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func headerOf(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") || !strings.HasSuffix(t, "]") {
		return "", false
	}
	inner := strings.TrimSpace(t[1 : len(t)-1])
	if strings.HasPrefix(inner, "[") { // [[数组段]] 当普通段处理
		inner = strings.TrimSpace(strings.TrimPrefix(inner, "["))
		inner = strings.TrimSuffix(inner, "]")
	}
	return inner, true
}

// tomlTopValue 读顶层键的原始值串（用于托管标记判断）。
func tomlTopValue(src, key string) string {
	section := ""
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if h, ok := headerOf(t); ok {
			section = h
			continue
		}
		if section != "" {
			continue
		}
		if k, v, ok := splitKV(t); ok && k == key {
			return v
		}
	}
	return ""
}

func splitKV(t string) (key, value string, ok bool) {
	// 键可能是 "quoted" 或 bare；找第一个不在引号内的 =
	inQuote := byte(0)
	for i := 0; i < len(t); i++ {
		c := t[i]
		if inQuote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inQuote = c
		case '=':
			return strings.TrimSpace(t[:i]), strings.TrimSpace(t[i+1:]), true
		}
	}
	return "", "", false
}

// tomlSetTop 设置顶层键（必须落在第一个段头之前）。
func tomlSetTop(src, key, value string) string {
	lines := strings.Split(src, "\n")
	assign := key + " = " + value
	firstHeader := -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if _, ok := headerOf(t); ok {
			firstHeader = i
			break
		}
		if k, _, ok := splitKV(t); ok && k == key {
			lines[i] = assign
			return strings.Join(lines, "\n")
		}
	}
	if firstHeader == -1 {
		trimmed := strings.TrimRight(src, "\n")
		if trimmed == "" {
			return assign + "\n"
		}
		return trimmed + "\n" + assign + "\n"
	}
	out := append([]string{}, lines[:firstHeader]...)
	out = append(out, assign)
	out = append(out, lines[firstHeader:]...)
	return strings.Join(out, "\n")
}

// tomlSetSection 整体替换（或追加）一个段的内容。kv 顺序即写出顺序。
func tomlSetSection(src, header string, kv [][2]string) string {
	lines := strings.Split(src, "\n")
	start := -1
	for i, line := range lines {
		if h, ok := headerOf(line); ok && h == header {
			start = i
			break
		}
	}
	body := make([]string, 0, len(kv))
	for _, pair := range kv {
		body = append(body, pair[0]+" = "+pair[1])
	}
	if start == -1 {
		trimmed := strings.TrimRight(src, "\n")
		block := "[" + header + "]\n" + strings.Join(body, "\n")
		if trimmed == "" {
			return block + "\n"
		}
		return trimmed + "\n\n" + block + "\n"
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if _, ok := headerOf(lines[i]); ok {
			end = i
			break
		}
	}
	out := append([]string{}, lines[:start+1]...)
	out = append(out, body...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}

// tomlRemoveSections 删除段头匹配 predicate 的整段（清过期托管模型用）。
func tomlRemoveSections(src string, predicate func(header string) bool) string {
	lines := strings.Split(src, "\n")
	var out []string
	skip := false
	for _, line := range lines {
		if h, ok := headerOf(line); ok {
			skip = predicate(h)
			if skip {
				continue
			}
		}
		if !skip {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
