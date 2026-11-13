package errclass_test

import (
	"strings"
	"testing"

	"commandcode-or-or-cliproxyapi/internal/adapter/shared"
	"commandcode-or-or-cliproxyapi/internal/errclass"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func TestLocalFailuresAreRequestScoped(t *testing.T) {
	_, malformed := shared.RewriteModelID("model", []byte("{"), "openai")
	_, args := shared.DecodeArgs("{")
	_, _, choice := shared.DecodeToolChoice([]byte(`{"type":"namespace"}`))
	for name, e := range map[string]*errclass.Error{
		"namespace":      shared.FunctionTool("namespace", "/v1/chat/completions"),
		"format":         shared.UnsupportedFormat("grpc", "/v1/messages"),
		"block":          shared.UnsupportedPartType("audio", "/v1/chat/completions"),
		"part":           shared.ValidateContentPart("audio", "/v1/messages"),
		"role":           shared.ValidateRole("robot", "/v1/messages"),
		"item":           shared.UnsupportedInputItemType("web_search"),
		"malformed_json": malformed,
		"malformed_args": args,
		"tool_choice":    choice,
		"generic_400":    errclass.FromStatus(400, "invalid parameter"),
		"translation":    errclass.Translation("invalid input: Bearer sk-secret123"),
	} {
		t.Run(name, func(t *testing.T) {
			if e == nil {
				t.Fatal("expected rejection")
			}
			wire := errclass.ToEnvelopeError(e)
			if e.StatusCode != 400 || e.WireCode != pluginabi.ErrorCodeRequestScoped || wire.HTTPStatus != 400 || !wire.IsRequestScoped() || wire.Retryable || wire.CredentialScoped || wire.RetryAfterSeconds != 0 {
				t.Fatalf("local envelope = %+v, want non-retryable request_scoped HTTP 400", wire)
			}
			if wire.Message == "" || strings.Contains(wire.Message, "sk-secret123") || strings.Contains(e.Message, "sk-secret123") {
				t.Fatalf("missing or unredacted message: %+v", wire)
			}
		})
	}
	e := shared.FunctionTool("namespace", "/v1/chat/completions")
	if e.Message != `unsupported tool type "namespace"; only function tools translate to /v1/chat/completions` {
		t.Fatalf("namespace message changed: %q", e.Message)
	}
	if e.Class != errclass.ClassUnsupported {
		t.Fatalf("namespace internal class = %q", e.Class)
	}
	if shared.FunctionTool("function", "/v1/chat/completions") != nil {
		t.Fatal("function tool rejected")
	}
}

func TestUpstreamRequestWordingPreservesFailureScope(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status           int
		body             string
		code             string
		credentialScoped bool
	}{
		{"authentication", 401, `{"error":{"message":"invalid key; max_tokens not allowed"}}`, string(errclass.ClassAuth), false},
		{"forbidden", 403, `{"error":{"message":"access denied; max_tokens not allowed"}}`, string(errclass.ClassAuth), false},
		{"suspended", 403, `{"error":{"message":"account suspended; max_tokens not allowed"}}`, pluginabi.ErrorCodeCredentialQuota, true},
		{"billing", 402, `{"error":{"message":"insufficient credits for max_tokens"}}`, pluginabi.ErrorCodeCredentialQuota, true},
		{"rate_limit", 429, `{"error":{"message":"rate limit exceeded for max_tokens per minute"}}`, pluginabi.ErrorCodeTransientRateLimit, false},
		{"quota", 429, `{"code":"RATE_LIMITED","message":"weekly usage limit for max_tokens"}`, pluginabi.ErrorCodeCredentialQuota, true},
		{"model", 400, `{"error":{"message":"model_not_in_plan; max_tokens not allowed"}}`, pluginabi.ErrorCodeModelNotFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := errclass.ToEnvelopeError(errclass.FromUpstream(tc.status, []byte(tc.body)))
			if wire.Code != tc.code || wire.CredentialScoped != tc.credentialScoped || !wire.Retryable || wire.IsRequestScoped() {
				t.Fatalf("upstream envelope = %+v, want code %q credential_scoped=%t and retryable", wire, tc.code, tc.credentialScoped)
			}
		})
	}
}
