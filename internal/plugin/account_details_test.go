package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestAccountDetailsReadbackRejectsIdentityChangesAndRedactsSecrets(t *testing.T) {
	for _, mode := range []string{"success", "changed_id", "changed_name", "deleted", "read_failed", "wrong_id", "invalid_request"} {
		t.Run(mode, func(t *testing.T) {
			entry := pluginapi.HostAuthFileEntry{ID: "stable", AuthIndex: "idx", Name: "a.json", Provider: ProviderID, Source: "file", Status: "active"}
			read := false
			f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
				switch method {
				case pluginabi.MethodHostAuthList:
					current := entry
					if read {
						switch mode {
						case "changed_id":
							current.ID = "replacement"
						case "changed_name":
							current.Name = "replacement.json"
						case "deleted":
							return hostOK(map[string]any{"files": []pluginapi.HostAuthFileEntry{}}), nil
						}
					}
					return hostOK(map[string]any{"files": []pluginapi.HostAuthFileEntry{current}}), nil
				case pluginabi.MethodHostAuthGet:
					read = true
					if mode == "read_failed" {
						return nil, context.DeadlineExceeded
					}
					return hostOK(pluginapi.HostAuthGetResponse{AuthIndex: "idx", Name: "a.json", JSON: json.RawMessage(`{"type":"commandcode","api_key":"details-key-secret","commandcode_proxy_id":"px_a","proxy_url":"http://proxy-user:proxy-password-secret@localhost:8080","custom_preserved":"private-metadata-secret"}`)}), nil
				default:
					t.Errorf("详情回读不应调用其他宿主接口：%s", method)
					return hostOK(map[string]any{}), nil
				}
			}}
			m := NewManager(NewHostBridge(f.call))
			body := `{"key_id":"idx","auth_id":"stable"}`
			if mode == "wrong_id" {
				body = `{"key_id":"idx","auth_id":"wrong"}`
			} else if mode == "invalid_request" {
				body = `{}`
			}
			response, err := m.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: "/v0/management/plugins/" + pluginName + "/account-details", Body: []byte(body)})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "success" {
				want := http.StatusConflict
				if mode == "read_failed" {
					want = http.StatusBadGateway
				} else if mode == "invalid_request" {
					want = http.StatusBadRequest
				}
				if response.StatusCode != want {
					t.Fatalf("status=%d want=%d body=%s", response.StatusCode, want, response.Body)
				}
				return
			}
			var card quotaCard
			if err := json.Unmarshal(response.Body, &card); err != nil {
				t.Fatal(err)
			}
			if card.AuthID != "stable" || card.KeyID != "idx" || card.ProxyID != "px_a" || card.ProxyURLHash == "" || !card.ProxyConfigured || card.ProxyReadError {
				t.Fatalf("详情回读不完整：%s", response.Body)
			}
			for _, secret := range []string{"details-key-secret", "proxy-password-secret", "proxy-user", "private-metadata-secret", `"proxy_url":`} {
				if strings.Contains(string(response.Body), secret) {
					t.Fatalf("详情接口泄漏私密字段：%s", secret)
				}
			}
		})
	}
}

func TestAccountProbeErrorClassificationAndRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		status    int
		code      string
		retryable bool
	}{
		{0, "network_failure", true}, {401, "credentials_rejected", false},
		{403, "credentials_rejected", false}, {429, "probe_rate_limited", true},
		{503, "upstream_rejected", true}, {400, "upstream_rejected", false},
	} {
		got := newAccountProbeError(accountSubscriptionPath, tc.status, http.Header{"Retry-After": []string{"120"}})
		if got.Phase != "subscriptions" || got.Code != tc.code || got.Retryable != tc.retryable || got.HTTPStatus != tc.status || got.RetryAfterSeconds != 120 {
			t.Fatalf("错误分类不符合只读检测语义：%+v", got)
		}
	}
	for _, tc := range []struct {
		raw      string
		min, max int
	}{
		{"999999", 86400, 86400}, {"-1", 0, 0}, {"nonsense", 0, 0},
		{time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat), 118, 120},
	} {
		got := newAccountProbeError(accountWhoamiPath, 429, http.Header{"Retry-After": []string{tc.raw}})
		if got.Phase != "identity" || got.RetryAfterSeconds < tc.min || got.RetryAfterSeconds > tc.max {
			t.Fatalf("Retry-After=%q got=%+v", tc.raw, got)
		}
	}
}
