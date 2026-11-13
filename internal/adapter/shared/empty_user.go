package shared

import (
	"bytes"
	"encoding/json"

	"commandcode-or-or-cliproxyapi/internal/errclass"
)

// RemoveEmptyUserContent 只清理确定无内容的 user 消息及空文本块。
// 不清理 assistant/tool 消息或 tool_result 的嵌套 content：空工具结果仍有
// 调用关联语义，图片和未知内容块也不能因为缺少 text 被误删。
// 无修改时原样返回，保留 native passthrough 的字节及全部未知字段。
func RemoveEmptyUserContent(body []byte, field string) ([]byte, *errclass.Error) {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil {
		return body, nil
	}
	raw, exists := root[field]
	if !exists {
		return body, nil
	}
	var turns []json.RawMessage
	if json.Unmarshal(raw, &turns) != nil || turns == nil {
		return body, nil
	}
	changed := false
	kept := make([]json.RawMessage, 0, len(turns))
	for _, turn := range turns {
		var message map[string]json.RawMessage
		if json.Unmarshal(turn, &message) != nil || message == nil {
			kept = append(kept, turn)
			continue
		}
		var role string
		if json.Unmarshal(message["role"], &role) != nil || role != "user" {
			kept = append(kept, turn)
			continue
		}
		// Responses 的其他 item 即使携带 role，也不属于普通 message。
		if field == "input" {
			var kind string
			if v, ok := message["type"]; ok && (json.Unmarshal(v, &kind) != nil || (kind != "" && kind != "message")) {
				kept = append(kept, turn)
				continue
			}
		}
		content, contentChanged, empty := removeEmptyTextParts(message["content"])
		if empty {
			changed = true
			continue
		}
		if contentChanged {
			message["content"] = content
			encoded, err := json.Marshal(message)
			if err != nil {
				return nil, errclass.Translation("cannot encode normalized user message")
			}
			turn = encoded
			changed = true
		}
		kept = append(kept, turn)
	}
	if !changed {
		return body, nil
	}
	root[field], _ = json.Marshal(kept)
	out, err := json.Marshal(root)
	if err != nil {
		return nil, errclass.Translation("cannot encode normalized request")
	}
	return out, nil
}

func removeEmptyTextParts(raw json.RawMessage) (json.RawMessage, bool, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return raw, false, true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return raw, false, text == ""
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || parts == nil {
		return raw, false, false
	}
	kept := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		var obj map[string]json.RawMessage
		if json.Unmarshal(part, &obj) == nil && obj != nil {
			var kind string
			_ = json.Unmarshal(obj["type"], &kind)
			if kind == "text" || kind == "input_text" || kind == "output_text" {
				value, ok := obj["text"]
				if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					continue
				}
				if json.Unmarshal(value, &text) == nil && text == "" {
					continue
				}
			}
		}
		kept = append(kept, part)
	}
	if len(kept) == 0 {
		return raw, len(parts) > 0, true
	}
	if len(kept) == len(parts) {
		return raw, false, false
	}
	out, _ := json.Marshal(kept)
	return out, true, false
}
