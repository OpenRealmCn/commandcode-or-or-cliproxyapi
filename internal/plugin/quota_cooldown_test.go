package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"commandcode-or-or-cliproxyapi/internal/errclass"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func testSnapshot(month *quotaWindow, weekly, five quotaWindow) quotaSnapshot {
	return quotaSnapshot{usage: quotaUsage{Month: month, Weekly: weekly, FiveHour: five}}
}
func healthyQuotaSnapshot() quotaSnapshot {
	return testSnapshot(&quotaWindow{Status: "ok", Cap: 70, Used: 1}, quotaWindow{Status: "ok", Cap: 35, Used: 1}, quotaWindow{Status: "ok", Cap: 14, Used: 1})
}
func cooldownTestClock(q *quotaCooldownManager) *atomic.Int64 {
	c := &atomic.Int64{}
	c.Store(time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC).UnixNano())
	q.now = func() time.Time { return time.Unix(0, c.Load()).UTC() }
	return c
}
func loadQuotaTest(t *testing.T, q *quotaCooldownManager) quotaSnapshot {
	t.Helper()
	s, e := q.load(context.Background(), "base", "key", false, true, time.Time{}, time.Time{})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func assertCooldown(t *testing.T, e *errclass.Error, seconds int, window string) {
	t.Helper()
	if e == nil || e.StatusCode != 429 || e.WireCode != pluginabi.ErrorCodeCredentialQuota || !e.CredentialScoped || !e.Retryable || e.RetryAfterSeconds != seconds || !strings.Contains(e.Message, window) {
		t.Fatalf("cooldown=%+v want %s/%ds", e, window, seconds)
	}
}

func TestDisplayedQuotaExhaustionUsesRoundedRemainingPercent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window quotaWindow
		want   bool
	}{
		{"13.98/14", quotaWindow{Status: "ok", Used: 13.98, Cap: 14}, true}, {"0.49%", quotaWindow{Status: "ok", Used: 99.51, Cap: 100}, true}, {"0.50%", quotaWindow{Status: "ok", Used: 99.5, Cap: 100}, false},
		{"at cap", quotaWindow{Status: "ok", Used: 14, Cap: 14}, true}, {"over cap", quotaWindow{Status: "ok", Used: 15, Cap: 14}, true}, {"flag", quotaWindow{Status: "exceeded", Used: 1, Cap: 14}, true},
		{"unlimited", quotaWindow{Status: "unlimited", Used: 14, Cap: 14}, false}, {"zero cap", quotaWindow{Status: "exceeded", Used: 10}, false}, {"negative", quotaWindow{Cap: -1}, false}, {"NaN", quotaWindow{Used: math.NaN(), Cap: 14}, false}, {"Inf", quotaWindow{Cap: math.Inf(1)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if g := displayedQuotaExhausted(tc.window); g != tc.want {
				t.Fatalf("got %v want %v", g, tc.want)
			}
		})
	}
	if displayedQuotaExhausted(*monthWindow(70, 0, 10)) {
		t.Fatal("purchased credits counted as exhausted")
	}
}

func TestQuotaCooldownPriorityAndDeadlinesDoNotSlide(t *testing.T) {
	s := healthyQuotaSnapshot()
	q := newQuotaCooldownManager(func(context.Context, string, string, bool) (quotaSnapshot, error) { return cloneQuotaSnapshot(s), nil })
	clock := cooldownTestClock(q)
	start := q.now()
	s.usage.FiveHour.Used = 13.98
	raw := loadQuotaTest(t, q)
	assertCooldown(t, q.cooldown("base", "key"), 18000, "5-hour")
	display := raw.display()
	if raw.usage.FiveHour.Status == "exceeded" || display.Month == raw.usage.Month {
		t.Fatal("display shares mutable source snapshot")
	}
	clock.Add(int64(time.Hour))
	loadQuotaTest(t, q)
	assertCooldown(t, q.cooldown("base", "key"), 14400, "5-hour")
	s = healthyQuotaSnapshot()
	loadQuotaTest(t, q)
	assertCooldown(t, q.cooldown("base", "key"), 14400, "5-hour")
	s.usage.Weekly.Used = 35
	loadQuotaTest(t, q)
	assertCooldown(t, q.cooldown("base", "key"), 604800, "weekly")
	s.usage.Month.Used = 70
	s.usage.Month.ResetsAt = start.Add(20 * 24 * time.Hour).Format(time.RFC3339)
	loadQuotaTest(t, q)
	assertCooldown(t, q.cooldown("base", "key"), 20*86400-3600, "monthly")
	clock.Add(int64(time.Hour))
	loadQuotaTest(t, q)
	assertCooldown(t, q.cooldown("base", "key"), 20*86400-7200, "monthly")
	if q.cooldown("other-base", "key") != nil || q.cooldown("base", "other-key") != nil {
		t.Fatal("credential/base state leaked")
	}
}

func TestQuotaCooldownMissingMonthlyDateDoesNotInventShortTimer(t *testing.T) {
	for _, d := range []string{"", "invalid", "2020-01-01T00:00:00Z"} {
		q := newQuotaCooldownManager(func(context.Context, string, string, bool) (quotaSnapshot, error) {
			s := healthyQuotaSnapshot()
			s.usage.Month.Used = 70
			s.usage.Month.ResetsAt = d
			return s, nil
		})
		cooldownTestClock(q)
		got := loadQuotaTest(t, q)
		if got.display().FiveHour.Status != "exceeded" || q.cooldown("base", "key") != nil {
			t.Fatalf("false cooldown for currentPeriodEnd=%q", d)
		}
	}
}

func TestQuotaCooldownExpiryRefreshesAndReevaluates(t *testing.T) {
	calls, spent := 0, true
	q := newQuotaCooldownManager(func(context.Context, string, string, bool) (quotaSnapshot, error) {
		calls++
		s := healthyQuotaSnapshot()
		if spent {
			s.usage.FiveHour.Used = 14
		}
		return s, nil
	})
	clock := cooldownTestClock(q)
	loadQuotaTest(t, q)
	clock.Add(int64(5 * time.Hour))
	if q.cooldown("base", "key") != nil {
		t.Fatal("deadline did not expire")
	}
	spent = false
	if _, e := q.load(context.Background(), "base", "key", false, false, time.Time{}, time.Time{}); e != nil {
		t.Fatal(e)
	}
	if calls != 2 || q.cooldown("base", "key") != nil {
		t.Fatal("old quota snapshot reused")
	}
	spent = true
	loadQuotaTest(t, q)
	assertCooldown(t, q.cooldown("base", "key"), 18000, "5-hour")
}

func TestQuotaCooldownTTLsIdentityAndFailureDoNotCool(t *testing.T) {
	calls, fail := 0, false
	q := newQuotaCooldownManager(func(_ context.Context, _, _ string, identity bool) (quotaSnapshot, error) {
		calls++
		if fail {
			return quotaSnapshot{}, errors.New("offline")
		}
		s := healthyQuotaSnapshot()
		s.identity = identity
		return s, nil
	})
	clock := cooldownTestClock(q)
	load := func(identity, force bool) {
		_, _ = q.load(context.Background(), "base", "key", identity, force, time.Time{}, time.Time{})
	}
	load(false, false)
	load(false, false)
	if calls != 1 {
		t.Fatal("healthy cache not reused")
	}
	load(true, false)
	if calls != 2 {
		t.Fatal("identity cache not refreshed")
	}
	clock.Add(int64(time.Minute))
	fail = true
	load(false, false)
	load(false, false)
	if calls != 3 {
		t.Fatal("failure ttl not reused")
	}
	clock.Add(int64(10 * time.Second))
	load(false, false)
	if calls != 4 || q.cooldown("base", "key") != nil {
		t.Fatal("quota lookup failure cooled account")
	}
	load(false, true)
	if calls != 5 {
		t.Fatal("forced lookup did not bypass cache")
	}
}

func TestQuotaCooldownSingleflightAndFailureBeforeLookupAnchor(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	q := newQuotaCooldownManager(func(ctx context.Context, _, _ string, _ bool) (quotaSnapshot, error) {
		if calls.Add(1) == 1 {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return quotaSnapshot{}, ctx.Err()
			}
		}
		return healthyQuotaSnapshot(), nil
	})
	clock := cooldownTestClock(q)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = q.load(context.Background(), "base", "key", false, false, time.Time{}, time.Time{})
	}()
	<-started
	clock.Add(int64(time.Second))
	failed := q.now()
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = q.load(context.Background(), "base", "key", false, true, failed, failed)
	}()
	close(release)
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("stale supplemental query reused: %d", calls.Load())
	}
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = q.load(context.Background(), "base", "key", false, false, time.Time{}, time.Time{})
		}()
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatal("concurrent health reads not merged")
	}
}

func TestQuotaCooldownResetCancelsAndCloseRejects(t *testing.T) {
	started := make(chan struct{}, 1)
	q := newQuotaCooldownManager(func(ctx context.Context, _, _ string, _ bool) (quotaSnapshot, error) {
		started <- struct{}{}
		<-ctx.Done()
		return healthyQuotaSnapshot(), ctx.Err()
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = q.load(context.Background(), "base", "key", false, true, time.Time{}, time.Time{})
	}()
	<-started
	q.reset()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reset did not cancel query")
	}
	if q.cooldown("base", "key") != nil {
		t.Fatal("cancelled query republished")
	}
	q.close()
	if _, e := q.load(context.Background(), "base", "key", false, true, time.Time{}, time.Time{}); !errors.Is(e, context.Canceled) {
		t.Fatalf("close error=%v", e)
	}
}

func TestQuotaPreflightBlocksUnaryAndStreamBeforeModelRequest(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "unary", true: "stream"}[stream], func(t *testing.T) {
			m, f := newExecManager(t)
			q := m.quotaState()
			q.fetch = func(context.Context, string, string, bool) (quotaSnapshot, error) {
				s := healthyQuotaSnapshot()
				s.usage.FiveHour.Used = 13.98
				return s, nil
			}
			clock := cooldownTestClock(q)
			beforeDo, beforeStream := len(f.callsOf(pluginabi.MethodHostHTTPDo)), len(f.callsOf(pluginabi.MethodHostHTTPDoStream))
			var env pluginabi.Envelope
			if stream {
				raw, e := m.HandleCall(pluginabi.MethodExecutorExecuteStream, execStreamReqBody("commandcode/glm-5.3", "openai", []byte(ccRequestBody), "down-quota"))
				if e != nil {
					t.Fatal(e)
				}
				env = decodeEnv(t, raw)
			} else {
				env = mustExecute(t, m, "commandcode/glm-5.3", "openai", []byte(ccRequestBody))
			}
			if env.OK || env.Error == nil || env.Error.HTTPStatus != 429 || !env.Error.CredentialScoped || env.Error.RetryAfterSeconds != 18000 {
				t.Fatalf("preflight=%+v", env.Error)
			}
			if len(f.callsOf(pluginabi.MethodHostHTTPDo)) != beforeDo || len(f.callsOf(pluginabi.MethodHostHTTPDoStream)) != beforeStream {
				t.Fatal("exhausted account reached model callback")
			}
			clock.Add(int64(time.Hour))
			assertCooldown(t, q.cooldown("https://api.commandcode.ai/provider/v1", testKey), 14400, "5-hour")
		})
	}
}

func TestQuotaFailureSupplementAnchorsAtFailureAndPreservesOriginal(t *testing.T) {
	m, _ := newExecManager(t)
	q := m.quotaState()
	clock := cooldownTestClock(q)
	q.fetch = func(context.Context, string, string, bool) (quotaSnapshot, error) {
		clock.Add(int64(2 * time.Second))
		s := healthyQuotaSnapshot()
		s.usage.FiveHour.Used = 14
		return s, nil
	}
	res := &resolvedExecution{key: "key"}
	res.cfg.BaseURL = "base"
	original := &errclass.Error{Class: errclass.ClassQuota, StatusCode: 429, WireCode: pluginabi.ErrorCodeCredentialQuota, CredentialScoped: true}
	if got := m.quotaFailure(context.Background(), res, original); got.RetryAfterSeconds != 17998 {
		t.Fatalf("supplement shifted anchor: %+v", got)
	}
	q.reset()
	q.fetch = func(context.Context, string, string, bool) (quotaSnapshot, error) {
		return quotaSnapshot{}, errors.New("offline")
	}
	if m.quotaFailure(context.Background(), res, original) != original {
		t.Fatal("failed supplement replaced original")
	}
	plain := &errclass.Error{StatusCode: 429, WireCode: pluginabi.ErrorCodeTransientRateLimit}
	if m.quotaFailure(context.Background(), res, plain) != plain {
		t.Fatal("plain 429 promoted to account quota")
	}
}

func TestObservedQuotaDisplaysHierarchyButDoesNotStartFakeTimers(t *testing.T) {
	f := &fakeCaller{}
	m := NewManager(NewHostBridge(f.call))
	t.Cleanup(func() { _, _ = m.HandleCall(pluginabi.MethodPluginShutdown, nil) })
	q := m.quotaState()
	cooldownTestClock(q)
	end := q.now().Add(10 * 24 * time.Hour).Format(time.RFC3339)
	f.responder = func(method string, payload []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		var req hostHTTPReq
		if e := json.Unmarshal(payload, &req); e != nil {
			return nil, e
		}
		body := `{}`
		switch {
		case strings.HasSuffix(req.URL, "/credits"):
			body = `{"credits":{"monthlyCredits":0,"purchasedCredits":0},"windowLimits":{"limited":true,"weekly":{"used":1,"cap":35},"fiveHour":{"used":1,"cap":14}}}`
		case strings.HasSuffix(req.URL, "/subscriptions"):
			body = `{"success":true,"data":{"planId":"individual-goat","currentPeriodEnd":"` + end + `"}}`
		case strings.Contains(req.URL, "/whoami"):
			body = `{"user":{"email":"test@example.test"}}`
		}
		return hostOK(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}), nil
	}
	u, _, _, e := m.observedQuota(context.Background(), "https://example.test/provider/v1", time.Second, "key")
	if e != nil || u.FiveHour.Status != "exceeded" {
		t.Fatalf("display usage=%+v err=%v", u, e)
	}
	q.mu.Lock()
	entry := q.entries[quotaStateKey("https://example.test/provider/v1", "key")]
	fake := !entry.deadlines[1].IsZero() || !entry.deadlines[2].IsZero()
	q.mu.Unlock()
	if fake {
		t.Fatal("monthly display fold triggered false short timer")
	}
}
