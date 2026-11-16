package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestAccountAvailabilityIsReadOnlyAndRequiresCompleteEvidence(t *testing.T) {
	for _, mode := range []string{"available", "disabled", "credentials_rejected", "partial", "limited", "server_failure", "subscription_unknown", "subscription_expired", "runtime_unknown", "runtime_restricted", "changed_id", "changed_key", "changed_base"} {
		t.Run(mode, func(t *testing.T) {
			entry := pluginapi.HostAuthFileEntry{ID: "stable", AuthIndex: "idx", Name: "a.json", Provider: ProviderID, Source: "file", Status: "active", Disabled: mode == "disabled"}
			var m *Manager
			queried := false
			f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
				switch method {
				case pluginabi.MethodHostAuthList:
					actual := entry
					if queried && mode == "changed_id" {
						actual.ID = "replaced"
					}
					return hostOK(map[string]any{"files": []pluginapi.HostAuthFileEntry{actual}}), nil
				case pluginabi.MethodHostAuthGet:
					key := "probe-key"
					if queried && mode == "changed_key" {
						key = "replacement-key"
					}
					return hostOK(pluginapi.HostAuthGetResponse{AuthIndex: "idx", Name: "a.json", JSON: json.RawMessage(`{"type":"commandcode","api_key":"` + key + `"}`)}), nil
				case pluginabi.MethodHostAuthGetRuntime:
					if mode == "runtime_unknown" {
						return []byte(`{"ok":false,"error":{"code":"unsupported","message":"unsupported"}}`), nil
					}
					actual := entry
					actual.Unavailable = mode == "runtime_restricted"
					return hostOK(pluginapi.HostAuthGetRuntimeResponse{Auth: actual}), nil
				case pluginabi.MethodHostHTTPDo:
					var req hostHTTPReq
					_ = json.Unmarshal(payload, &req)
					if mode == "credentials_rejected" {
						queried = true
						return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusUnauthorized, Headers: http.Header{}, Body: []byte(`{}`)}), nil
					}
					if mode == "server_failure" {
						queried = true
						return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusServiceUnavailable, Headers: http.Header{}, Body: []byte(`{}`)}), nil
					}
					body := `{"credits":{"monthlyCredits":70,"purchasedCredits":0},"windowLimits":{"limited":true,"weekly":{"used":1,"cap":35},"fiveHour":{"used":1,"cap":14}}}`
					if mode == "limited" {
						body = `{"credits":{"monthlyCredits":70,"purchasedCredits":0},"windowLimits":{"limited":true,"weekly":{"used":35,"cap":35},"fiveHour":{"used":1,"cap":14}}}`
					}
					if mode == "partial" {
						body = `{"credits":{"monthlyCredits":70,"purchasedCredits":0},"windowLimits":{"limited":true,"weekly":{"used":1,"cap":35}}}`
					}
					if strings.HasSuffix(req.URL, "/subscriptions") {
						if mode == "subscription_unknown" {
							queried = true
							return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{}`)}), nil
						}
						end := "2099-01-01T00:00:00Z"
						if mode == "subscription_expired" {
							end = "2020-01-01T00:00:00Z"
						}
						body = `{"success":true,"data":{"planId":"individual-goat","status":"active","currentPeriodEnd":"` + end + `"}}`
					}
					queried = true
					if mode == "changed_base" {
						m.mu.Lock()
						m.cfg.BaseURL = "https://other.test/provider/v1"
						m.mu.Unlock()
					}
					return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(body)}), nil
				default:
					return hostOK(map[string]any{}), nil
				}
			}}
			m = NewManager(NewHostBridge(f.call))
			m.cfg.BaseURL = "https://example.test/provider/v1"
			body := []byte(`{"key_id":"idx","auth_id":"stable"}`)
			resp, err := m.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: "/v0/management/plugins/" + pluginName + "/account-availability", Body: body})
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "changed_") {
				if resp.StatusCode != http.StatusConflict {
					t.Fatalf("stale result accepted: %s", resp.Body)
				}
				return
			}
			if resp.StatusCode >= 400 {
				t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
			}
			var got accountAvailability
			if err := json.Unmarshal(resp.Body, &got); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"available": "available", "disabled": "disabled", "credentials_rejected": "unavailable", "partial": "unknown", "limited": "unavailable", "server_failure": "unknown", "subscription_unknown": "unknown", "subscription_expired": "unavailable", "runtime_unknown": "unknown", "runtime_restricted": "unavailable"}[mode]
			if got.State != want {
				t.Fatalf("state=%q want %q response=%s", got.State, want, resp.Body)
			}
			if got.ModelVerified || got.AccountEgressVerified {
				t.Fatal("non-billed probe claimed model or egress verification")
			}
			var paths []string
			for _, call := range f.recorded() {
				switch call.method {
				case pluginabi.MethodHostAuthList, pluginabi.MethodHostAuthGet, pluginabi.MethodHostAuthGetRuntime:
				case pluginabi.MethodHostHTTPDo:
					var req hostHTTPReq
					_ = json.Unmarshal(call.payload, &req)
					if req.Method != http.MethodGet || req.URL != "https://example.test/alpha/billing/"+"credits" && req.URL != "https://example.test/alpha/billing/subscriptions" {
						t.Fatalf("unexpected network request: %s %s", req.Method, req.URL)
					}
					paths = append(paths, req.URL)
				default:
					t.Fatalf("unexpected host callback in read-only probe: %s", call.method)
				}
			}
			if mode == "disabled" {
				if len(paths) != 0 {
					t.Fatal("disabled account was queried upstream")
				}
			} else if mode == "credentials_rejected" || mode == "server_failure" {
				if len(paths) != 1 || !strings.HasSuffix(paths[0], "/credits") {
					t.Fatalf("credential rejection paths=%v", paths)
				}
			} else if len(paths) != 2 || !strings.HasSuffix(paths[0], "/credits") || !strings.HasSuffix(paths[1], "/subscriptions") {
				t.Fatalf("unexpected read-only paths: %v", paths)
			}
		})
	}
}
