package shared

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRemoveEmptyUserContentPreservesUnchangedRequests(t *testing.T) {
	for _, body := range []string{
		` { "model": "m", "messages": [{"role":"user","content":" keep \n "}], "custom": {"unknown":true} } `,
		`{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"t"}]},{"role":"tool","tool_call_id":"t","content":""}]}`,
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":""}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"future_block","payload":{"value":1}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":42}]}]}`,
		`{"messages":[{"role":"user","content":42}]}`,
		`{"messages":[{"role":"user","content":[null]}]}`,
		`{"messages":"invalid-array-shape"}`,
		`{"messages":[`,
		`null`,
	} {
		out, err := RemoveEmptyUserContent([]byte(body), "messages")
		if err != nil || !bytes.Equal(out, []byte(body)) {
			t.Fatalf("unchanged request rewritten: %q -> %q, %v", body, out, err)
		}
	}
}

func TestRemoveEmptyUserContentKeepsMeaningfulBlocksAndMetadata(t *testing.T) {
	body := []byte(`{"model":"m","custom":{"keep":1},"tools":[{"name":"f"}],"messages":[{"role":"user","content":null},{"role":"assistant","content":null,"custom":"assistant-kept"},{"role":"user","custom":"message-kept","content":[{"type":"text","text":""},{"type":"text"},{"type":"text","text":null},{"type":"text","text":" keep ","cache_control":{"type":"ephemeral"}},{"type":"image_url","image_url":{"url":"https://example.test/image.png","detail":"high"}},{"type":"tool_result","tool_use_id":"t","content":""},{"type":"future_block","payload":"unknown-kept"}]},{"role":"tool","tool_call_id":"t","content":""}]}`)
	out, err := RemoveEmptyUserContent(body, "messages")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	if string(root["custom"]) != `{"keep":1}` || string(root["tools"]) != `[{"name":"f"}]` {
		t.Fatalf("top-level metadata lost: %s", out)
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(root["messages"], &messages); err != nil || len(messages) != 3 {
		t.Fatalf("messages lost: %s, %v", out, err)
	}
	if string(messages[0]["content"]) != "null" || string(messages[0]["custom"]) != `"assistant-kept"` || string(messages[2]["content"]) != `""` {
		t.Fatalf("assistant/tool messages modified: %s", out)
	}
	if string(messages[1]["custom"]) != `"message-kept"` {
		t.Fatalf("user metadata lost: %s", out)
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(messages[1]["content"], &parts); err != nil || len(parts) != 4 {
		t.Fatalf("meaningful blocks removed: %s, %v", out, err)
	}
	if string(parts[0]["text"]) != `" keep "` || string(parts[0]["cache_control"]) != `{"type":"ephemeral"}` ||
		string(parts[1]["image_url"]) != `{"url":"https://example.test/image.png","detail":"high"}` ||
		string(parts[2]["tool_use_id"]) != `"t"` || string(parts[2]["content"]) != `""` ||
		string(parts[3]["payload"]) != `"unknown-kept"` {
		t.Fatalf("nonempty content/order altered: %s", out)
	}
}

func TestRemoveEmptyUserContentPreservesResponsesNonMessageItems(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":""},{"type":"function_call_output","call_id":"t","output":"","role":"user"},{"type":"reasoning","role":"user","summary":[]},{"type":"future_item","role":"user","payload":1},{"type":"message","role":"user","content":[{"type":"input_text","text":""},{"type":"input_image","image_url":"https://example.test/image.png"}]}]}`)
	out, err := RemoveEmptyUserContent(body, "input")
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(out, &root); err != nil || len(root.Input) != 4 {
		t.Fatalf("Responses non-message item removed: %s, %v", out, err)
	}
	for i, want := range []string{`"function_call_output"`, `"reasoning"`, `"future_item"`, `"message"`} {
		if string(root.Input[i]["type"]) != want {
			t.Fatalf("item order/content lost: %s", out)
		}
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(root.Input[3]["content"], &parts); err != nil || len(parts) != 1 || string(parts[0]["type"]) != `"input_image"` {
		t.Fatalf("Responses image lost: %s", out)
	}
}
