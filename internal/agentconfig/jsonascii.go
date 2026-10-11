package agentconfig

import (
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// ─────────────────────────────────────────────────────────────────────────────
// 备份文件的 JSON 序列化
//
// 参照实现把备份写成
//
//	json.dumps(new_state, indent=2, ensure_ascii=True).encode("utf-8") + b"\n"
//	（agent_config.py:186）
//
// 注意是 **ensure_ascii=True**，与写 Agent 配置时用的 ensure_ascii=False 相反：
// target_path 可能含中文用户名，此时必须输出 \uXXXX 转义。canonical 包的编码器
// 只覆盖 ensure_ascii=False（它就是为此设计的，见 canonical/encode.go），所以这里
// 单独实现 ensure_ascii=True 的缩进形式——不做这一步，Go 与 Python 写出的备份
// 在含非 ASCII 路径的机器上会逐字节不同。
//
// 键序即插入顺序（不排序），与 Python dict 一致。
// ─────────────────────────────────────────────────────────────────────────────

// newBackupBytes 生成备份文件内容（含末尾换行）。
//
// version 3 起不再写 `mode` 字段：集成没有「路由模式」了，三个客户端一律注入全部
// 可用模型。旧备份（v1/v2，含 mode）仍能被读取与回退——多出来的键不影响解析。
func newBackupBytes(
	agent, targetPath string,
	originalExists bool,
	originalContent string,
	appliedSHA256 string,
	extra []extraBackupEntry,
) []byte {
	pairs := []canonical.ObjectPair{
		{Key: "version", Value: canonical.NewIntValue(backupStateVersion)},
		{Key: "agent", Value: canonical.NewString(agent)},
		{Key: "target_path", Value: canonical.NewString(targetPath)},
		{Key: "original_exists", Value: canonical.NewBool(originalExists)},
		{Key: "original_content", Value: canonical.NewString(originalContent)},
		{Key: "applied_sha256", Value: canonical.NewString(appliedSHA256)},
	}
	if len(extra) > 0 {
		items := make([]*canonical.Value, 0, len(extra))
		for _, entry := range extra {
			items = append(items, canonical.NewObjectOf(
				canonical.ObjectPair{Key: "target_path", Value: canonical.NewString(entry.TargetPath)},
				canonical.ObjectPair{Key: "original_exists", Value: canonical.NewBool(entry.OriginalExists)},
				canonical.ObjectPair{Key: "original_content", Value: canonical.NewString(entry.OriginalContent)},
				canonical.ObjectPair{Key: "applied_sha256", Value: canonical.NewString(entry.AppliedSHA256)},
			))
		}
		pairs = append(pairs, canonical.ObjectPair{Key: "extra_targets", Value: canonical.NewArray(items...)})
	}
	return []byte(dumpJSONIndentASCII(canonical.NewObjectOf(pairs...)) + "\n")
}

// dumpJSONIndentASCII 等价于 `json.dumps(obj, indent=2, ensure_ascii=True)`。
func dumpJSONIndentASCII(value *canonical.Value) string {
	var b strings.Builder
	encodeASCIIValue(&b, value, 2, 0)
	return b.String()
}

// encodeASCIIValue 按键插入顺序写出值，每层缩进 indent 个空格。
func encodeASCIIValue(b *strings.Builder, value *canonical.Value, indent, depth int) {
	if value == nil {
		b.WriteString("null")
		return
	}
	switch value.Kind {
	case canonical.KindNull:
		b.WriteString("null")
	case canonical.KindBool:
		if value.Bool {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case canonical.KindNumber:
		b.WriteString(value.Num)
	case canonical.KindString:
		encodeASCIIString(b, value.Str)
	case canonical.KindArray:
		if value.Len() == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, item := range value.Items() {
			if i > 0 {
				b.WriteByte(',')
			}
			writeASCIIIndent(b, indent, depth+1)
			encodeASCIIValue(b, item, indent, depth+1)
		}
		writeASCIIIndent(b, indent, depth)
		b.WriteByte(']')
	case canonical.KindObject:
		keys := value.Obj.Keys()
		if len(keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeASCIIIndent(b, indent, depth+1)
			encodeASCIIString(b, key)
			b.WriteString(": ")
			encodeASCIIValue(b, value.Lookup(key), indent, depth+1)
		}
		writeASCIIIndent(b, indent, depth)
		b.WriteByte('}')
	default:
		b.WriteString("null")
	}
}

// writeASCIIIndent 换行并写出当前层缩进。
func writeASCIIIndent(b *strings.Builder, indent, depth int) {
	b.WriteByte('\n')
	for i := 0; i < indent*depth; i++ {
		b.WriteByte(' ')
	}
}

// encodeASCIIString 按 Python 的 ensure_ascii=True 规则转义：
// 非 0x20..0x7E 的字符一律转义，> 0xFFFF 的用 UTF-16 代理对。
func encodeASCIIString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r >= 0x20 && r <= 0x7e {
				b.WriteRune(r)
				continue
			}
			writeUnicodeEscape(b, r)
		}
	}
	b.WriteByte('"')
}

// writeUnicodeEscape 写出 \uXXXX（必要时写代理对），十六进制小写。
func writeUnicodeEscape(b *strings.Builder, r rune) {
	const hexDigits = "0123456789abcdef"
	writeUnit := func(unit rune) {
		b.WriteString(`\u`)
		b.WriteByte(hexDigits[(unit>>12)&0xf])
		b.WriteByte(hexDigits[(unit>>8)&0xf])
		b.WriteByte(hexDigits[(unit>>4)&0xf])
		b.WriteByte(hexDigits[unit&0xf])
	}
	if r > 0xffff {
		r -= 0x10000
		writeUnit(0xd800 + (r >> 10))
		writeUnit(0xdc00 + (r & 0x3ff))
		return
	}
	writeUnit(r)
}
