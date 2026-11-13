package adapters

import (
	"encoding/json"
	"testing"

	"commandcode-or-or-cliproxyapi/internal/adapter/chatcompletions"
	"commandcode-or-or-cliproxyapi/internal/adapter/messages"
	"commandcode-or-or-cliproxyapi/internal/adapter/responses"
	"commandcode-or-or-cliproxyapi/internal/errclass"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestEmptyUserMessagesAcrossRoutes(t *testing.T) {
	builders := map[string]func(string, string, []byte, *pluginapi.ThinkingSupport) ([]byte, *errclass.Error){
		"chat": chatcompletions.BuildRequest, "messages": messages.BuildRequest, "responses": responses.BuildRequest,
	}
	bodies := map[string]string{
		"openai":          `{"model":"m","custom":"keep","messages":[{"role":"user","content":"hi"},{"role":"user","content":""},{"role":"user","content":null},{"role":"user","content":[]},{"role":"user","content":[{"type":"text","text":""}]},{"role":"user","content":[{"type":"text","text":""},{"type":"text","text":" keep "}]}]}`,
		"claude":          `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"hi"},{"role":"user","content":""},{"role":"user","content":null},{"role":"user","content":[]},{"role":"user","content":[{"type":"text","text":""}]},{"role":"user","content":[{"type":"text","text":""},{"type":"text","text":" keep "}]}]}`,
		"openai-response": `{"model":"m","input":[{"role":"user","content":"hi"},{"role":"user","content":""},{"role":"user","content":null},{"role":"user","content":[]},{"role":"user","content":[{"type":"input_text","text":""}]},{"role":"user","content":[{"type":"input_text","text":""},{"type":"input_text","text":" keep "}]}]}`,
	}
	for target, build := range builders {
		for source, body := range bodies {
			t.Run(target+"/"+source, func(t *testing.T) {
				out, e := build("m", source, []byte(body), nil)
				if e != nil {
					t.Fatal(e)
				}
				var wire map[string]json.RawMessage
				if err := json.Unmarshal(out, &wire); err != nil {
					t.Fatal(err)
				}
				field := "messages"
				if target == "responses" {
					field = "input"
				}
				var turns []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				}
				if err := json.Unmarshal(wire[field], &turns); err != nil {
					t.Fatal(err)
				}
				text := ""
				for _, turn := range turns {
					if turn.Role != "user" {
						continue
					}
					var s string
					if json.Unmarshal(turn.Content, &s) == nil {
						if s == "" {
							t.Fatalf("empty user: %s", out)
						}
						text += s
						continue
					}
					var parts []map[string]any
					if err := json.Unmarshal(turn.Content, &parts); err != nil || len(parts) == 0 {
						t.Fatalf("empty/invalid parts: %s", out)
					}
					for _, p := range parts {
						if p["text"] == "" {
							t.Fatalf("empty text part: %s", out)
						}
						if s, ok := p["text"].(string); ok {
							text += s
						}
					}
				}
				if text != "hi keep " {
					t.Fatalf("nonempty text changed: %q: %s", text, out)
				}
			})
		}
	}
}

func TestEmptyTextBesideToolResultPreservesTool(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"test","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":""},{"type":"text","text":""}]}],"tools":[{"name":"test","input_schema":{"type":"object"}}]}`)
	out, e := chatcompletions.BuildRequest("m", "claude", body, nil)
	if e != nil {
		t.Fatal(e)
	}
	var wire struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Messages) != 3 {
		t.Fatalf("unexpected extra empty user or lost tool: %s", out)
	}
	tool := wire.Messages[2]
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != "" {
		t.Fatalf("tool changed: %s", out)
	}
}
