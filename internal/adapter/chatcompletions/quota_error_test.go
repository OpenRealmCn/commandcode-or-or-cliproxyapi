package chatcompletions

import (
	"fmt"
	"testing"
	"time"

	"commandcode-or-or-cliproxyapi/internal/errclass"
)

func TestStreamQuotaErrors(t *testing.T) {
	reset := time.Now().Add(time.Hour).Unix()
	for _, target := range []string{"openai", "claude", "openai-response"} {
		for _, tc := range []struct {
			name, detail string
			class        errclass.Class
			scope, reset bool
			status       int
		}{
			{"rate_limited", fmt.Sprintf(`"code":"RATE_LIMITED","message":"slow down","rateLimit":{"window":"weekly","reset":%d}`, reset), errclass.ClassQuota, true, true, 429},
			{"rate_limit_only", fmt.Sprintf(`"rateLimit":{"window":"five_hour","reset":%d}`, reset), errclass.ClassQuota, true, true, 429},
			{"usage_exceeded", `"code":"USAGE_EXCEEDED","message":"spent"`, errclass.ClassBilling, true, false, 429},
			{"plain_429", `"code":429,"message":"slow down"`, errclass.ClassRateLimit, false, false, 429},
			{"generic_rate_limit", `"code":"rate_limit_exceeded","message":"slow down"`, errclass.ClassRateLimit, false, false, 429},
			{"unknown", `"type":"server_error","message":"opaque failure"`, errclass.ClassUpstream, false, false, 0},
			{"empty_rate_limit", `"rateLimit":{}`, errclass.ClassUpstream, false, false, 0},
		} {
			t.Run(target+"/"+tc.name, func(t *testing.T) {
				sc := NewStreamConverter(target)
				wire := `data: {"error":{` + tc.detail + "}}\n\n"
				var got *errclass.Error
				for offset := 0; offset < len(wire); {
					end := min(offset+7, len(wire))
					events, done, eErr := sc.Feed([]byte(wire[offset:end]))
					if len(events) != 0 || done {
						t.Fatalf("error emitted success: events=%q done=%v", events, done)
					}
					offset = end
					if eErr != nil {
						got = eErr
						break
					}
				}
				if got == nil || got.Class != tc.class || got.StatusCode != tc.status || got.CredentialScoped != tc.scope || !got.Retryable {
					t.Fatalf("unexpected classification: %+v", got)
				}
				if tc.scope && got.WireCode != "credential_quota" {
					t.Fatalf("lost quota wire code: %+v", got)
				}
				if tc.class == errclass.ClassRateLimit && got.WireCode != "transient_rate_limit" {
					t.Fatalf("plain rate limit became credential quota: %+v", got)
				}
				if tc.reset && (got.RetryAfterSeconds < 3590 || got.RetryAfterSeconds > 3600) {
					t.Fatalf("lost reset hint: %+v", got)
				}
			})
		}
	}
}

func TestStreamErrorInspectionPreservesOpenAIPassthrough(t *testing.T) {
	for _, payload := range []string{
		`{ "id" : "x", "choices" : [{"delta":{"content":"hi"}}], "vendor":true }`,
		`{"choices":[{"delta":{"content":{"unexpected":"shape"}}}]}`,
		`{malformed ordinary payload`,
		`{"error":null,"choices":[]}`,
		`{"error":"vendor extension"}`,
	} {
		t.Run(payload, func(t *testing.T) {
			sc := NewStreamConverter("openai")
			wire := "data: " + payload + "\r\n\r\n"
			var all [][]byte
			for i := range len(wire) {
				events, done, eErr := sc.Feed([]byte(wire[i : i+1]))
				if done || eErr != nil {
					t.Fatalf("passthrough changed: done=%v err=%+v", done, eErr)
				}
				all = append(all, events...)
			}
			if len(all) != 1 || string(all[0]) != payload {
				t.Fatalf("payload bytes changed: %q", all)
			}
		})
	}
}
