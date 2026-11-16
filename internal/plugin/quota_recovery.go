package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const cooldownRecoveryWarning = "此操作会重置该账号及其所有模型的冷却，可能包括其他错误产生的限制。CPA 没有条件式重置接口，核对后新产生的限制也可能被清除。确认继续？"

type quotaRecoveryTicket struct {
	token, authID, authIndex, name, base, keyHash string
	generation                                    uint64
	expires                                       time.Time
}

type quotaRecoveryRequest struct {
	KeyID   string `json:"key_id"`
	AuthID  string `json:"auth_id"`
	Token   string `json:"token"`
	Confirm bool   `json:"confirm"`
}

// 只允许管理鉴权路由调用。预览票据一次性使用，不改变 disabled/auth 文件。
func (m *Manager) handleCooldownRecovery(ctx context.Context, body []byte) (pluginapi.ManagementResponse, error) {
	var req quotaRecoveryRequest
	if len(body) > 4096 || json.Unmarshal(body, &req) != nil || req.KeyID == "" || req.AuthID == "" {
		return managementError(http.StatusBadRequest, "恢复请求格式无效，请刷新账户列表")
	}
	m.lifeMu.Lock()
	defer m.lifeMu.Unlock()
	q := m.quotaState()
	var ticket quotaRecoveryTicket
	if req.Confirm {
		q.mu.Lock()
		ticket = q.recoveryTickets[req.Token]
		delete(q.recoveryTickets, req.Token) // 失败及超时也不可自动重发该票据。
		q.mu.Unlock()
		if ticket.token == "" || !ticket.expires.After(time.Now()) || ticket.authIndex != req.KeyID || ticket.authID != req.AuthID {
			return managementError(http.StatusConflict, "恢复确认已过期或已使用，请重新核对")
		}
	} else if req.Token != "" {
		return managementError(http.StatusBadRequest, "请重新核对恢复操作")
	}
	entry, ok := m.authEntryByIndex(ctx, req.KeyID)
	if !ok || entry.ID != req.AuthID || entry.Disabled || entry.Status == "disabled" {
		return managementError(http.StatusConflict, "账号已删除、禁用或身份已改变，请刷新列表")
	}
	key, err := authFileKey(ctx, m.bridge, entry)
	if err != nil {
		return managementError(http.StatusBadGateway, "无法读取账号凭据")
	}
	m.mu.RLock()
	base := m.cfg.BaseURL
	m.mu.RUnlock()
	if req.Confirm && (ticket.base != base || ticket.keyHash != authKeyHash(key) || ticket.name != entry.Name) {
		return managementError(http.StatusConflict, "账号凭据或配置已改变，请重新核对")
	}
	// after 使预览/确认不能加入此前开始的旧查询。
	q.mu.Lock()
	started := q.now()
	state := q.entryLocked(quotaStateKey(base, key), started)
	generation := state.generation
	q.mu.Unlock()
	if req.Confirm && generation != ticket.generation {
		return managementError(http.StatusConflict, "核对后出现新的额度错误，请重新核对")
	}
	s, err := q.load(ctx, base, key, false, true, started, time.Time{})
	if err != nil {
		return recoveryError(http.StatusBadGateway, probeError(err, "credits").Code, "无法获取新鲜额度，未执行冷却重置")
	}
	if !quotaSnapshotHealthy(s) {
		code, message := "incomplete_evidence", "订阅或额度数据不完整，无法确认恢复，未执行冷却重置"
		window := quotaBlockingWindow(s)
		if window != "" {
			code, message = "quota_exhausted", window+" 窗口仍耗尽（剩余取整为0%），未执行冷却重置"
		}
		if s.usage.SubscriptionState == "expired" || s.usage.SubscriptionState == "inactive" {
			code, message = "subscription_invalid", "订阅无效或已到期，未执行冷却重置"
		}
		usage := s.display()
		usage.LocalCooldown = q.diagnostic(base, key)
		out, err := quotaJSON(map[string]any{"error": message, "reason_code": code, "blocking_window": window, "usage": usage})
		out.StatusCode = http.StatusConflict
		return out, err
	}
	// 查询结束后再次读取实际文件及运行时摘要，不以缓存卡片作为身份凭据。
	current, found := m.authEntryByIndex(ctx, req.KeyID)
	if !found || current.ID != entry.ID || current.Name != entry.Name {
		return managementError(http.StatusConflict, "账号身份已改变，请重新核对")
	}
	currentKey, err := authFileKey(ctx, m.bridge, current)
	if err != nil || currentKey != key {
		return managementError(http.StatusConflict, "账号凭据已改变，请重新核对")
	}
	runtime, err := m.bridge.AuthRuntime(ctx, req.KeyID)
	if err != nil {
		return recoveryError(http.StatusBadGateway, "runtime_unavailable", "无法核对宿主运行时状态；当前宿主可能不支持恢复接口")
	}
	if runtime.ID != entry.ID || !isCommandcodeCredential(runtime) || runtime.Disabled || runtime.Status == "disabled" || !pluginQuotaCooldownMessage(runtime.StatusMessage) {
		return recoveryError(http.StatusConflict, "runtime_not_quota_cooldown", "当前状态不是插件配额冷却，或账号已禁用；未执行重置")
	}
	q.mu.Lock()
	unchanged := q.entries[quotaStateKey(base, key)] == state && state.generation == generation && state.err == nil && state.cached && quotaSnapshotHealthy(state.snapshot)
	q.mu.Unlock()
	if !unchanged {
		return managementError(http.StatusConflict, "核对期间额度状态已改变，请重新核对")
	}
	if !req.Confirm {
		bytes := make([]byte, 32)
		if _, err := rand.Read(bytes); err != nil {
			return managementError(http.StatusInternalServerError, "无法生成恢复确认")
		}
		token := hex.EncodeToString(bytes)
		q.mu.Lock()
		if q.recoveryTickets == nil {
			q.recoveryTickets = make(map[string]quotaRecoveryTicket)
		}
		for k, t := range q.recoveryTickets {
			if !t.expires.After(time.Now()) || t.authIndex == req.KeyID {
				delete(q.recoveryTickets, k)
			}
		}
		if len(q.recoveryTickets) >= 256 {
			q.mu.Unlock()
			return managementError(http.StatusTooManyRequests, "待确认恢复操作过多，请稍后重试")
		}
		q.recoveryTickets[token] = quotaRecoveryTicket{token: token, authID: entry.ID, authIndex: req.KeyID, name: entry.Name, base: base, keyHash: authKeyHash(key), generation: generation, expires: time.Now().Add(2 * time.Minute)}
		q.mu.Unlock()
		return quotaJSON(map[string]any{"token": token, "warning": cooldownRecoveryWarning, "usage": s.display(), "local_cooldown": q.diagnostic(base, key)})
	}
	// 宿主无 CAS：上述检查减少错误目标，但不构成并发安全保证。
	if err := m.bridge.ResetCooldown(ctx, req.KeyID); err != nil {
		readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = m.bridge.AuthRuntime(readCtx, req.KeyID)
		cancel()
		return managementError(http.StatusBadGateway, "宿主冷却重置未获确认，结果可能未知；请刷新宿主状态，不要自动重试")
	}
	q.mu.Lock()
	if q.entries[quotaStateKey(base, key)] == state && state.generation == generation {
		state.recovered = false
	}
	q.mu.Unlock()
	runtime, err = m.bridge.AuthRuntime(ctx, req.KeyID)
	if err != nil || runtime.Unavailable || runtime.Disabled {
		return quotaJSON(map[string]any{"reset": true, "message": "宿主已确认重置；账号仍可能存在其他限制，请刷新管理中心核对。"})
	}
	return quotaJSON(map[string]any{"reset": true, "message": "宿主已确认重置该账号冷却，请刷新管理中心；后续可用性仍由真实请求决定。"})
}

func recoveryError(status int, code, message string) (pluginapi.ManagementResponse, error) {
	out, err := quotaJSON(map[string]any{"error": message, "reason_code": code})
	out.StatusCode = status
	return out, err
}

func pluginQuotaCooldownMessage(msg string) bool {
	for _, window := range []string{"monthly", "weekly", "5-hour"} {
		prefix := window + " quota exhausted; account cooldown until "
		if suffix, found := strings.CutPrefix(msg, prefix); found {
			reset := strings.TrimSpace(suffix)
			if at, err := time.Parse(time.RFC3339, reset); err == nil && !at.IsZero() {
				return true
			}
		}
		if msg == window+" usage window exhausted" || strings.HasPrefix(msg, window+" usage window exhausted: ") {
			return true
		}
	}
	return false
}
