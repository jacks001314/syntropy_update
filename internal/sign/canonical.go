// Package sign 实现与桌面端 update-service.ts 字节一致的规范化 JSON 与 Ed25519 签名。
//
// 桌面客户端对清单 payload 的签名对象是 canonicalize() 的输出：对象键排序、无空白、
// 字符串按 JS JSON.stringify 语义转义。服务端必须逐字节复刻，否则签名校验失败。
// 一致性由 tests/vectors 下的跨语言测试向量保证。
package sign

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Canonicalize 返回与 JS canonicalize() 一致的规范化 JSON 串。
//
// 输入应当是 json.Marshal 产物或等价的标准 JSON 文本。数字使用 json.Number 解码以
// 保留原始字面量（与 JS 的数值打印保持一致）。
func Canonicalize(raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("decode payload: %w", err)
	}
	if decoder.More() {
		return "", fmt.Errorf("decode payload: trailing content")
	}
	var buf strings.Builder
	if err := writeCanonical(&buf, value); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func writeCanonical(buf *strings.Builder, value any) error {
	switch typed := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if typed {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeJSString(buf, typed)
	case json.Number:
		number, err := canonicalNumber(typed)
		if err != nil {
			return err
		}
		buf.WriteString(number)
	case []any:
		buf.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		// JS 用 localeCompare 排序。清单 payload 的键全部是小写字母开头的 ASCII，
		// 此时 localeCompare 与码元序一致；向量测试覆盖了这一点。
		sort.Strings(keys)
		buf.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buf.WriteByte(',')
			}
			writeJSString(buf, key)
			buf.WriteByte(':')
			if err := writeCanonical(buf, typed[key]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("canonicalize: unsupported value type %T", value)
	}
	return nil
}

// canonicalNumber 复刻 JS JSON.stringify 对数字的打印。清单里的数字都是非负整数
// （size），其余数值形态直接报错，避免悄悄产出与 JS 不同的文本。
func canonicalNumber(number json.Number) (string, error) {
	text := number.String()
	if strings.ContainsAny(text, ".eE") {
		return "", fmt.Errorf("canonicalize: non-integer number %q is not supported", text)
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return "", fmt.Errorf("canonicalize: parse number %q: %w", text, err)
	}
	return strconv.FormatInt(value, 10), nil
}

// writeJSString 按 ECMA-262 JSON.stringify 的字符串转义规则输出。
//
// 与 Go encoding/json 的差异：不转义 <、>、&（Go 默认 HTML 转义），非 ASCII 原样输出
// UTF-8，控制字符 < 0x20 用 \u00xx（小写十六进制）。
func writeJSString(buf *strings.Builder, value string) {
	buf.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				buf.WriteString(`\u00`)
				buf.WriteByte(hexDigits[byte(r)>>4])
				buf.WriteByte(hexDigits[byte(r)&0x0f])
				continue
			}
			if r == utf8.RuneError {
				// 非法 UTF-8 或孤立代理项：Go 侧已由解码器替换为 U+FFFD。
				// 清单字段应为 ASCII，这里保守原样输出替换字符。
			}
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
}

const hexDigits = "0123456789abcdef"
