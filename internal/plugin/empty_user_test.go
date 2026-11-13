package plugin

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// 确认修复实际进入两种执行路径的出站 body，而非只在适配器直接调用时有效。
func TestExecuteFiltersEmptyUserBesideToolResult(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"test","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":""},{"type":"text","text":""}]}],"tools":[{"name":"test","input_schema":{"type":"object"}}]}`)
	for _, stream := range []bool{false, true} {
		name := "unary"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var m *Manager
			var f *fakeCaller
			method := pluginabi.MethodHostHTTPDo
			if stream {
				m, f = newStreamManager(t, streamScript{upstreamID: "up-empty-user", frames: []string{"data: [DONE]\n\n"}})
				method = pluginabi.MethodHostHTTPDoStream
				raw, err := m.HandleCall(pluginabi.MethodExecutorExecuteStream, execStreamReqBody("commandcode/glm-5.3", "claude", body, "down-empty-user"))
				if err != nil {
					t.Fatal(err)
				}
				if env := decodeEnv(t, raw); !env.OK {
					t.Fatalf("execute stream: %+v", env.Error)
				}
				if !m.bridge.WaitForInFlight(5 * time.Second) {
					t.Fatal("stream did not drain")
				}
			} else {
				m, f = newExecManager(t)
				if env := mustExecute(t, m, "commandcode/glm-5.3", "claude", body); !env.OK {
					t.Fatalf("execute: %+v", env.Error)
				}
			}
			upstream := wireBody(t, lastWire(t, f, method), "body")
			var wire struct {
				Messages []struct {
					Role       string `json:"role"`
					Content    any    `json:"content"`
					ToolCallID string `json:"tool_call_id"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(upstream, &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Messages) != 3 || wire.Messages[0].Role != "user" || wire.Messages[0].Content != "hi" ||
				wire.Messages[1].Role != "assistant" || wire.Messages[2].Role != "tool" ||
				wire.Messages[2].ToolCallID != "call_1" || wire.Messages[2].Content != "" {
				t.Fatalf("empty user emitted or tool result lost: %s", upstream)
			}
		})
	}
}
