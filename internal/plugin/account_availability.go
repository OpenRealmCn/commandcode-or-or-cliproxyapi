package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Account API failures are observations, never executor cooldown instructions.
type accountProbeError struct {
	Phase             string `json:"phase"`
	Code              string `json:"code"`
	HTTPStatus        int    `json:"http_status,omitempty"`
	Retryable         bool   `json:"retryable"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}

func (e *accountProbeError) Error() string {
	switch e.Code {
	case "credentials_rejected":
		return "账号接口拒绝凭据，请核对账号状态"
	case "probe_rate_limited":
		return "账号检测接口限流，请稍后重试"
	case "network_failure":
		return "账号检测网络失败或超时"
	default:
		return "账号检测数据无效或上游接口不可用"
	}
}

func newAccountProbeError(path string, status int, headers http.Header) *accountProbeError {
	phase := "credits"
	if strings.Contains(path, "subscriptions") {
		phase = "subscriptions"
	}
	if strings.Contains(path, "whoami") {
		phase = "identity"
	}
	e := &accountProbeError{Phase: phase, Code: "upstream_rejected", HTTPStatus: status, Retryable: status >= 500}
	switch status {
	case 0:
		e.Code, e.Retryable = "network_failure", true
	case 401, 403:
		e.Code = "credentials_rejected"
	case 429:
		e.Code, e.Retryable = "probe_rate_limited", true
	}
	if raw := headers.Get("Retry-After"); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil {
			if end, parseErr := http.ParseTime(raw); parseErr == nil {
				seconds = int(time.Until(end).Seconds())
			}
		}
		if seconds > 86400 {
			seconds = 86400
		}
		if seconds > 0 {
			e.RetryAfterSeconds = seconds
		}
	}
	return e
}

func probeError(err error, phase string) *accountProbeError {
	if found, ok := errors.AsType[*accountProbeError](err); ok {
		copy := *found
		return &copy
	}
	return &accountProbeError{Phase: phase, Code: "invalid_response", Retryable: true}
}

type accountCheckRequest struct {
	KeyID  string `json:"key_id"`
	AuthID string `json:"auth_id"`
}

type accountAvailability struct {
	KeyID                 string                  `json:"key_id"`
	AuthID                string                  `json:"auth_id"`
	State                 string                  `json:"state"`
	Reasons               []string                `json:"reason_codes"`
	CheckedAt             string                  `json:"checked_at"`
	Subscription          string                  `json:"subscription"`
	Quota                 string                  `json:"quota"`
	Runtime               string                  `json:"runtime"`
	RuntimeUntil          string                  `json:"runtime_until,omitempty"`
	LocalCooldown         quotaCooldownDiagnostic `json:"local_cooldown"`
	BlockingWindow        string                  `json:"blocking_window,omitempty"`
	Usage                 *quotaUsage             `json:"usage,omitempty"`
	Errors                []*accountProbeError    `json:"errors"`
	ModelVerified         bool                    `json:"model_verified"`
	AccountEgressVerified bool                    `json:"account_egress_verified"`
}

func parseAccountCheck(raw []byte) (accountCheckRequest, bool) {
	var req accountCheckRequest
	ok := len(raw) <= 4096 && json.Unmarshal(raw, &req) == nil && req.KeyID != "" && req.AuthID != ""
	return req, ok
}

// handleAccountDetails supports bounded single-account readback after narrow PATCH.
func (m *Manager) handleAccountDetails(ctx context.Context, raw []byte) (pluginapi.ManagementResponse, error) {
	req, ok := parseAccountCheck(raw)
	if !ok {
		return managementError(http.StatusBadRequest, "账号读取请求无效，请刷新列表")
	}
	entry, found := m.authEntryByIndex(ctx, req.KeyID)
	if !found || entry.ID != req.AuthID {
		return managementError(http.StatusConflict, "账号身份已改变，请刷新列表")
	}
	card, _, err := m.accountDetails(ctx, entry)
	if err != nil {
		return managementError(http.StatusBadGateway, "无法读取账号设置")
	}
	current, found := m.authEntryByIndex(ctx, req.KeyID)
	if !found || current.ID != entry.ID || current.Name != entry.Name {
		return managementError(http.StatusConflict, "账号身份已改变，请刷新列表")
	}
	return quotaJSON(card)
}

// This path deliberately bypasses observedQuota and email persistence. It must
// never change routing, cooldown generations, credentials, or auth files.
func (m *Manager) handleAccountAvailability(ctx context.Context, raw []byte) (pluginapi.ManagementResponse, error) {
	req, ok := parseAccountCheck(raw)
	if !ok {
		return managementError(http.StatusBadRequest, "检测请求无效，请刷新列表")
	}
	entry, found := m.authEntryByIndex(ctx, req.KeyID)
	if !found || entry.ID != req.AuthID {
		return managementError(http.StatusConflict, "账号身份已改变，请刷新列表")
	}
	out := accountAvailability{KeyID: req.KeyID, AuthID: req.AuthID, State: "unknown", Reasons: []string{}, Errors: []*accountProbeError{}, Subscription: "unknown", Quota: "unknown", Runtime: "unknown"}
	finish := func() (pluginapi.ManagementResponse, error) {
		out.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		return quotaJSON(out)
	}
	if entry.Disabled || entry.Status == "disabled" {
		out.State = "disabled"
		out.Reasons = append(out.Reasons, "disabled")
		return finish()
	}
	key, err := authFileKey(ctx, m.bridge, entry)
	if err != nil {
		out.Reasons = append(out.Reasons, "credential_read_failed")
		return finish()
	}
	m.mu.RLock()
	base, timeout, q := m.cfg.BaseURL, m.cfg.RequestTimeout, m.quotaCooldown
	m.mu.RUnlock()
	usage, _, _, fetchErr := fetchQuotaRaw(ctx, m.bridge, base, timeout, key, false)
	if fetchErr != nil {
		e := probeError(fetchErr, "credits")
		out.Errors = append(out.Errors, e)
		out.Reasons = append(out.Reasons, e.Code)
		if e.Code == "credentials_rejected" {
			out.State = "unavailable"
		}
	} else {
		out.Usage = &usage
		out.Subscription = usage.SubscriptionState
		if usage.SubscriptionError != nil {
			out.Errors = append(out.Errors, usage.SubscriptionError)
			out.Reasons = append(out.Reasons, usage.SubscriptionError.Code)
		}
		out.BlockingWindow = quotaBlockingWindow(quotaSnapshot{usage: usage})
		if out.BlockingWindow != "" {
			out.Quota = "exhausted"
			out.State = "unavailable"
			out.Reasons = append(out.Reasons, "quota_exhausted")
		} else if quotaSnapshotHealthy(quotaSnapshot{usage: usage}) {
			out.Quota = "healthy"
		}
		if usage.SubscriptionState == "expired" || usage.SubscriptionState == "inactive" {
			out.State = "unavailable"
			out.Reasons = append(out.Reasons, "subscription_invalid")
		}
		if usage.SubscriptionError != nil && usage.SubscriptionError.Code == "credentials_rejected" {
			out.State = "unavailable"
		}
	}
	if q != nil {
		out.LocalCooldown = q.diagnostic(base, key)
	}
	if out.LocalCooldown.Active {
		out.State = "unavailable"
		out.Reasons = append(out.Reasons, "local_cooldown")
	}
	runtime, runtimeErr := m.bridge.AuthRuntime(ctx, req.KeyID)
	if runtimeErr != nil {
		out.Reasons = append(out.Reasons, "runtime_unknown")
	} else if runtime.ID != entry.ID || !isCommandcodeCredential(runtime) {
		return managementError(http.StatusConflict, "账号身份已改变，请重新检测")
	} else {
		out.Runtime = "clear"
		if runtime.Disabled || runtime.Status == "disabled" {
			out.State, out.Runtime = "disabled", "disabled"
			out.Reasons = append(out.Reasons, "disabled")
		} else if runtime.Unavailable || runtime.Status == "error" || runtime.NextRetryAfter.After(time.Now()) {
			out.State, out.Runtime = "unavailable", "restricted"
			out.Reasons = append(out.Reasons, "host_restricted")
		}
		if runtime.NextRetryAfter.After(time.Now()) {
			out.RuntimeUntil = runtime.NextRetryAfter.UTC().Format(time.RFC3339)
		}
	}
	current, found := m.authEntryByIndex(ctx, req.KeyID)
	if !found || current.ID != entry.ID || current.Name != entry.Name {
		return managementError(http.StatusConflict, "账号身份已改变，请重新检测")
	}
	currentKey, keyErr := authFileKey(ctx, m.bridge, current)
	m.mu.RLock()
	sameBase := m.cfg.BaseURL == base
	m.mu.RUnlock()
	if keyErr != nil || currentKey != key || !sameBase {
		return managementError(http.StatusConflict, "账号凭据或配置已改变，请重新检测")
	}
	if current.Disabled || current.Status == "disabled" {
		out.State = "disabled"
		out.Reasons = append(out.Reasons, "disabled")
	}
	if out.State == "unknown" && out.Quota == "healthy" && out.Runtime == "clear" && !out.LocalCooldown.Active {
		out.State = "available"
	}
	if out.State == "unknown" && len(out.Reasons) == 0 {
		out.Reasons = append(out.Reasons, "incomplete_evidence")
	}
	return finish()
}

func quotaBlockingWindow(s quotaSnapshot) string {
	for i, w := range []*quotaWindow{s.usage.Month, &s.usage.Weekly, &s.usage.FiveHour} {
		if w != nil && !w.unknown && displayedQuotaExhausted(*w) {
			return [3]string{"monthly", "weekly", "5-hour"}[i]
		}
	}
	return ""
}
