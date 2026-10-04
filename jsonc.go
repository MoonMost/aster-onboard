package main

// JSONC/JSON5 -lite 处理：opencode.jsonc、openclaw.json 允许注释。
// 做法照搬参考项目的 extract_json5_comments：字符串外提取 // 与 /* */ 注释，
// 解析前剥掉、写回时把注释原样贴回文件头，用户的手写注释不丢。

import (
	"strings"
)

func walkJSONC(content string, collect func(comment string)) string {
	bytes := []byte(content)
	var sb strings.Builder
	var comment strings.Builder
	inComment := false
	block := false
	var quote byte
	escaped := false
	flush := func() {
		if inComment {
			collect(comment.String())
			comment.Reset()
			inComment = false
		}
	}
	for i := 0; i < len(bytes); i++ {
		c := bytes[i]
		if inComment {
			comment.WriteByte(c)
			if block {
				if c == '*' && i+1 < len(bytes) && bytes[i+1] == '/' {
					comment.WriteByte('/')
					i++
					flush()
				}
			} else if c == '\n' {
				flush()
				sb.WriteByte(c)
			}
			continue
		}
		if quote != 0 {
			sb.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '"' || c == '\'':
			quote = c
			sb.WriteByte(c)
		case c == '/' && i+1 < len(bytes) && bytes[i+1] == '/':
			inComment, block = true, false
			comment.WriteString("//")
			i++
		case c == '/' && i+1 < len(bytes) && bytes[i+1] == '*':
			inComment, block = true, true
			comment.WriteString("/*")
			i++
		default:
			sb.WriteByte(c)
		}
	}
	flush()
	return sb.String()
}

// extractJSONCComments 取出全部注释行（写回时贴文件头）。
func extractJSONCComments(content string) []string {
	var comments []string
	walkJSONC(content, func(c string) { comments = append(comments, c) })
	return comments
}

// stripJSONC 返回可被 encoding/json 解析的纯 JSON 文本。
func stripJSONC(raw []byte) []byte {
	return []byte(walkJSONC(string(raw), func(string) {}))
}
