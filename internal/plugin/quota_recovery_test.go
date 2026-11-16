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

func TestQuotaCooldownHealthyRecoveryAndUnknownData(t *testing.T) {
	s := healthyQuotaSnapshot()
	q := newQuotaCooldownManager(func(context.Context, string, string, bool) (quotaSnapshot, error) { return cloneQuotaSnapshot(s), nil })
	clock := cooldownTestClock(q)
	s.usage.Weekly.Used = 35
	loadQuotaTest(t, q)
	assertCooldown(t, q.cooldown("base", "key"), 604800, "weekly")
	clock.Add(int64(time.Minute))
	s.usage.Weekly = quotaWindow{Status: "ok", unknown: true}
	loadQuotaTest(t, q)
	assertCooldown(t, q.cooldown("base", "key"), 604740, "weekly")
	s = healthyQuotaSnapshot()
	s.usage.FiveHour.Used = 14
	loadQuotaTest(t, q)
	assertCooldown(t, q.cooldown("base", "key"), 18000, "5-hour")
	s = healthyQuotaSnapshot()
	loadQuotaTest(t, q)
	if q.cooldown("base", "key") != nil {
		t.Fatal("healthy snapshot did not cancel stale timer")
	}
}

func TestQuotaCooldownPreflightRechecksLiveTimer(t *testing.T) {
	m, _ := newExecManager(t)
	q := m.quotaState()
	clock := cooldownTestClock(q)
	s := healthyQuotaSnapshot()
	s.usage.Weekly.Used = 35
	q.fetch = func(context.Context, string, string, bool) (quotaSnapshot, error) { return s, nil }
	res := &resolvedExecution{key: "key"}
	res.cfg.BaseURL = "base"
	assertCooldown(t, m.preflightQuota(context.Background(), res), 604800, "weekly")
	s = healthyQuotaSnapshot()
	clock.Add(int64(time.Minute))
	if err := m.preflightQuota(context.Background(), res); err != nil {
		t.Fatal(err)
	}
}

func TestPluginQuotaCooldownMessageOnlyMatchesKnownWindowFormats(t *testing.T) {
	for _, message := range []string{
		"monthly quota exhausted; account cooldown until 2026-11-15T09:00:00Z",
		"weekly usage window exhausted",
		"5-hour usage window exhausted: limit reached",
	} {
		if !pluginQuotaCooldownMessage(message) {
			t.Errorf("known quota cooldown not recognized: %q", message)
		}
	}
	for _, message := range []string{"429 quota exhausted", "weekly quota exhausted; account cooldown until nonsense", "daily usage window exhausted", "weekly model quota exhausted"} {
		if pluginQuotaCooldownMessage(message) {
			t.Errorf("non-target cooldown recognized: %q", message)
		}
	}
}

func TestQuotaRecoveryErrorHasStructuredBlockingWindow(t *testing.T) {
	entry := pluginapi.HostAuthFileEntry{ID: "stable", AuthIndex: "idx", Name: "a.json", Provider: ProviderID, Source: "file", Status: "error", Unavailable: true, StatusMessage: "weekly quota exhausted; account cooldown until 2099-01-01T00:00:00Z"}
	f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			return hostOK(map[string]any{"files": []pluginapi.HostAuthFileEntry{entry}}), nil
		case pluginabi.MethodHostAuthGet:
			return hostOK(pluginapi.HostAuthGetResponse{AuthIndex: "idx", JSON: json.RawMessage(`{"type":"commandcode","api_key":"test-key-123"}`)}), nil
		case pluginabi.MethodHostHTTPDo:
			return hostOK(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"credits":{"monthlyCredits":70,"purchasedCredits":0},"windowLimits":{"limited":true,"weekly":{"used":35,"cap":35},"fiveHour":{"used":1,"cap":14}}}`)}), nil
		default:
			return hostOK(map[string]any{}), nil
		}
	}}
	m := NewManager(NewHostBridge(f.call))
	m.cfg.BaseURL = "https://example.test/provider/v1"
	response, err := m.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: "/v0/management/plugins/" + pluginName + "/cooldown-recovery", Body: []byte(`{"key_id":"idx","auth_id":"stable"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["reason_code"] != "quota_exhausted" || body["blocking_window"] != "weekly" {
		t.Fatalf("response=%v", body)
	}
	usage, ok := body["usage"].(map[string]any)
	if !ok || usage["weekly"] == nil {
		t.Fatalf("missing latest usage: %v", body)
	}
}

func TestQuotaRecoveryErrorsHaveStructuredReasons(t *testing.T) {
	for _, mode := range []string{"network_failure", "credentials_rejected", "runtime_unavailable", "runtime_not_quota_cooldown"} {
		t.Run(mode, func(t *testing.T) {
			entry := pluginapi.HostAuthFileEntry{ID: "stable", AuthIndex: "idx", Name: "a.json", Provider: ProviderID, Source: "file", Status: "error", Unavailable: true, StatusMessage: "weekly quota exhausted; account cooldown until 2099-01-01T00:00:00Z"}
			f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
				switch method {
				case pluginabi.MethodHostAuthList:
					return hostOK(map[string]any{"files": []pluginapi.HostAuthFileEntry{entry}}), nil
				case pluginabi.MethodHostAuthGet:
					return hostOK(pluginapi.HostAuthGetResponse{AuthIndex: "idx", JSON: json.RawMessage(`{"type":"commandcode","api_key":"test-key-123"}`)}), nil
				case pluginabi.MethodHostAuthGetRuntime:
					if mode == "runtime_unavailable" {
						return []byte(`{"ok":false,"error":{"code":"unsupported"}}`), nil
					}
					if mode == "runtime_not_quota_cooldown" {
						entry.StatusMessage = "unauthorized"
					}
					return hostOK(pluginapi.HostAuthGetRuntimeResponse{Auth: entry}), nil
				case pluginabi.MethodHostHTTPDo:
					if mode == "network_failure" {
						return nil, context.DeadlineExceeded
					}
					status := http.StatusOK
					body := `{"credits":{"monthlyCredits":70,"purchasedCredits":0},"windowLimits":{"limited":true,"weekly":{"used":1,"cap":35},"fiveHour":{"used":1,"cap":14}}}`
					var req hostHTTPReq
					_ = json.Unmarshal(payload, &req)
					if strings.HasSuffix(req.URL, "/subscriptions") {
						body = `{"success":true,"data":{"planId":"individual-goat","status":"active","currentPeriodEnd":"2099-01-01T00:00:00Z"}}`
					}
					if mode == "credentials_rejected" {
						status = http.StatusUnauthorized
						body = `{}`
					}
					return hostOK(pluginapi.HTTPResponse{StatusCode: status, Body: []byte(body)}), nil
				}
				return hostOK(map[string]any{}), nil
			}}
			m := NewManager(NewHostBridge(f.call))
			m.cfg.BaseURL = "https://example.test/provider/v1"
			response, err := m.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: "/v0/management/plugins/" + pluginName + "/cooldown-recovery", Body: []byte(`{"key_id":"idx","auth_id":"stable"}`)})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"network_failure": "network_failure", "credentials_rejected": "credentials_rejected", "runtime_unavailable": "runtime_unavailable", "runtime_not_quota_cooldown": "runtime_not_quota_cooldown"}[mode]
			if response.StatusCode < 400 {
				t.Fatalf("expected failure: %s", response.Body)
			}
			var got map[string]any
			if err := json.Unmarshal(response.Body, &got); err != nil {
				t.Fatal(err)
			}
			if got["reason_code"] != want {
				t.Fatalf("reason_code=%v want=%s body=%s", got["reason_code"], want, response.Body)
			}
		})
	}
}

func TestQuotaRecoveryRequiresConfirmationFreshHealthAndIdentity(t *testing.T) {
	for _, mode := range []string{"success", "exhausted", "missing_month", "changed_key", "changed_id", "disabled", "other_error", "reset_failure", "new_failure", "partial", "expired"} {
		t.Run(mode, func(t *testing.T) {
			entry := pluginapi.HostAuthFileEntry{ID: "stable", AuthIndex: "idx", Name: "a.json", Provider: ProviderID, Source: "file", Status: "error", Unavailable: true, StatusMessage: "weekly quota exhausted; account cooldown until 2099-01-01T00:00:00Z"}
			key := "test-key-123"
			resets := 0
			f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
				switch method {
				case pluginabi.MethodHostAuthList:
					return hostOK(map[string]any{"files": []pluginapi.HostAuthFileEntry{entry}}), nil
				case pluginabi.MethodHostAuthGet:
					return hostOK(pluginapi.HostAuthGetResponse{AuthIndex: "idx", JSON: json.RawMessage(`{"type":"commandcode","api_key":"` + key + `"}`)}), nil
				case pluginabi.MethodHostAuthGetRuntime:
					return hostOK(pluginapi.HostAuthGetRuntimeResponse{Auth: entry}), nil
				case pluginabi.MethodHostRoutingResetCooldown:
					resets++
					if mode == "reset_failure" {
						return []byte(`{"ok":false,"error":{"code":"failure","message":"secret"}}`), nil
					}
					entry.Unavailable = false
					entry.StatusMessage = ""
					return hostOK(pluginapi.HostRoutingResetCooldownResponse{AuthIndex: "idx"}), nil
				}
				return hostOK(map[string]any{}), nil
			}}
			m := NewManager(NewHostBridge(f.call))
			m.cfg.BaseURL = "https://example.test/provider/v1"
			q := m.quotaState()
			snapshot := healthyQuotaSnapshot()
			snapshot.usage.recoveryVerified = true
			q.fetch = func(context.Context, string, string, bool) (quotaSnapshot, error) { return snapshot, nil }
			call := func(r quotaRecoveryRequest) pluginapi.ManagementResponse {
				b, _ := json.Marshal(r)
				out, err := m.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: "/v0/management/plugins/" + pluginName + "/cooldown-recovery", Body: b})
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			preview := call(quotaRecoveryRequest{KeyID: "idx", AuthID: "stable"})
			if preview.StatusCode >= 400 {
				t.Fatalf("preview=%s", preview.Body)
			}
			if resets != 0 {
				t.Fatal("preview reset host")
			}
			var v struct {
				Token   string `json:"token"`
				Warning string `json:"warning"`
			}
			json.Unmarshal(preview.Body, &v)
			if v.Token == "" || !strings.Contains(v.Warning, "所有模型") {
				t.Fatal("missing confirmation scope")
			}
			switch mode {
			case "exhausted":
				snapshot.usage.Weekly.Used = 35
			case "missing_month":
				snapshot.usage.Month = nil
			case "changed_key":
				key = "changed-456"
			case "changed_id":
				entry.ID = "changed"
			case "disabled":
				entry.Disabled = true
			case "other_error":
				entry.StatusMessage = "unauthorized"
			case "partial":
				snapshot.usage.recoveryVerified = false
			case "expired":
				q.mu.Lock()
				expired := q.recoveryTickets[v.Token]
				expired.expires = time.Now().Add(-time.Second)
				q.recoveryTickets[v.Token] = expired
				q.mu.Unlock()
			case "new_failure":
				q.mu.Lock()
				q.entries[quotaStateKey(m.cfg.BaseURL, key)].generation++
				q.mu.Unlock()
			}
			confirm := quotaRecoveryRequest{KeyID: "idx", AuthID: "stable", Token: v.Token, Confirm: true}
			result := call(confirm)
			wantReset := mode == "success" || mode == "reset_failure"
			if wantReset && resets != 1 || !wantReset && resets != 0 {
				t.Fatalf("unexpected reset count %d", resets)
			}
			if mode == "success" {
				if result.StatusCode >= 400 {
					t.Fatalf("confirm=%s", result.Body)
				}
			} else if result.StatusCode < 400 {
				t.Fatalf("unsafe confirmation accepted: %s", result.Body)
			}
			if again := call(confirm); again.StatusCode != http.StatusConflict {
				t.Fatalf("ticket reused: %+v", again)
			}
		})
	}
}

func TestQuotaRecoveryRejectsPartialUpstreamSnapshot(t *testing.T) {
	for _, body := range []string{`{}`, `{"credits":{"monthlyCredits":50,"purchasedCredits":0},"windowLimits":{"limited":true,"weekly":{"used":0,"cap":35}}}`, `{"credits":{"monthlyCredits":50},"windowLimits":{"limited":true,"weekly":{"used":0,"cap":35},"fiveHour":{"used":0,"cap":14}}}`} {
		f := &fakeCaller{responder: func(method string, payload []byte) ([]byte, error) {
			var r hostHTTPReq
			json.Unmarshal(payload, &r)
			b := body
			if strings.HasSuffix(r.URL, "/subscriptions") {
				b = `{"success":true,"data":{"planId":"individual-goat","status":"active","currentPeriodEnd":"2099-01-01T00:00:00Z"}}`
			}
			return hostOK(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(b)}), nil
		}}
		u, _, _, err := fetchQuotaRaw(context.Background(), NewHostBridge(f.call), "https://example.test/provider/v1", time.Second, "key", false)
		if err != nil {
			t.Fatal(err)
		}
		if quotaSnapshotHealthy(quotaSnapshot{usage: u}) {
			t.Fatalf("partial data unlocked: %s", body)
		}
	}
}

func TestQuotaCooldownOldHealthyQueryCannotClearNewFailure(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	q := newQuotaCooldownManager(func(context.Context, string, string, bool) (quotaSnapshot, error) {
		calls++
		s := healthyQuotaSnapshot()
		if calls == 1 {
			close(started)
			<-release
		} else {
			s.usage.Weekly.Used = 35
		}
		return s, nil
	})
	cooldownTestClock(q)
	q.mu.Lock()
	entry := q.entryLocked(quotaStateKey("base", "key"), q.now())
	entry.deadlines[1] = q.now().Add(7 * 24 * time.Hour)
	q.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = q.load(context.Background(), "base", "key", false, true, time.Time{}, time.Time{})
	}()
	<-started
	q.mu.Lock()
	entry.generation++
	entry.cached = false
	q.mu.Unlock()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("query stuck")
	}
	assertCooldown(t, q.cooldown("base", "key"), 604800, "weekly")
	if calls != 2 {
		t.Fatalf("old query was not discarded: %d", calls)
	}
}
