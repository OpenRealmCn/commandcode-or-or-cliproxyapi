package responses

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"commandcode-or-or-cliproxyapi/internal/errclass"
)

func TestStreamCredentialQuotaFailures(t *testing.T) {
	reset := time.Now().Add(time.Hour).Unix()
	for _, target := range []string{"openai", "claude", "openai-response"} {
		for _, event := range []string{"error", "response.failed"} {
			for _, nested := range []bool{false, true} {
				for _, status := range []int{0, 429, 403} {
					for _, tc := range []struct {
						name, detail string
						class        errclass.Class
						reset        bool
					}{
						{"rate_limited", fmt.Sprintf(`{"code":"RATE_LIMITED","message":"wait","rateLimit":{"window":"weekly","reset":%d}}`, reset), errclass.ClassQuota, true},
						{"usage_exceeded", `{"code":"USAGE_EXCEEDED","message":"spent"}`, errclass.ClassBilling, false},
						{"rate_limit_only", fmt.Sprintf(`{"rateLimit":{"reset":%d}}`, reset), errclass.ClassQuota, true},
					} {
						name := fmt.Sprintf("%s/%s/nested=%v/status=%d/%s", target, event, nested, status, tc.name)
						t.Run(name, func(t *testing.T) {
							errorEnvelope := map[string]any{"error": json.RawMessage(tc.detail)}
							if status != 0 {
								errorEnvelope["status_code"] = status
							}
							var payload any = errorEnvelope
							if nested {
								payload = map[string]any{"response": errorEnvelope}
							}
							raw, err := json.Marshal(payload)
							if err != nil {
								t.Fatal(err)
							}
							events, done, got := runStream(t, target, frame(event, string(raw)))
							if len(events) != 0 || done {
								t.Fatalf("failure emitted success: events=%q done=%v", events, done)
							}
							if got == nil || got.Class != tc.class || got.StatusCode != 429 || !got.CredentialScoped || got.WireCode != "credential_quota" || !got.Retryable {
								t.Fatalf("unexpected classification: %+v", got)
							}
							if tc.reset && (got.RetryAfterSeconds < 3590 || got.RetryAfterSeconds > 3600) {
								t.Fatalf("lost reset hint: %+v", got)
							}
						})
					}
				}
			}
		}
	}
}

func TestStreamQuotaFailureFallbacks(t *testing.T) {
	for _, target := range []string{"openai", "claude", "openai-response"} {
		for _, tc := range []struct {
			name, payload, message string
			class                  errclass.Class
			status                 int
		}{
			{"top_level_unknown", `{"message":"opaque failure"}`, "opaque failure", errclass.ClassUpstream, 0},
			{"nested_unknown", `{"response":{"error":{"code":"server_error","message":"nested failure"}}}`, "nested failure", errclass.ClassUpstream, 0},
			{"top_error_unknown", `{"error":{"code":"server_error","message":"opaque"}}`, "", errclass.ClassUpstream, 0},
			{"plain_429", `{"status_code":429,"error":{"message":"slow down"}}`, "slow down", errclass.ClassRateLimit, 429},
			{"nested_plain_429", `{"response":{"status_code":429,"error":{"message":"slow down"}}}`, "slow down", errclass.ClassRateLimit, 429},
			{"no_status_rate_limit", `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`, "", errclass.ClassUpstream, 0},
			{"empty_rate_limit", `{"error":{"rateLimit":{}}}`, "", errclass.ClassUpstream, 0},
		} {
			t.Run(target+"/"+tc.name, func(t *testing.T) {
				events, done, got := runStream(t, target, frame("response.failed", tc.payload))
				if len(events) != 0 || done || got == nil || got.Class != tc.class || got.StatusCode != tc.status || got.CredentialScoped || !got.Retryable || got.RetryAfterSeconds != 0 {
					t.Fatalf("fallback changed: events=%q done=%v err=%+v", events, done, got)
				}
				if got.Message != tc.message {
					t.Fatalf("message changed: %q want %q", got.Message, tc.message)
				}
			})
		}
	}
}
