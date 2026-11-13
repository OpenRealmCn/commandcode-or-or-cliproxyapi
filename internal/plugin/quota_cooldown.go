package plugin

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"commandcode-or-or-cliproxyapi/internal/errclass"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

const (
	quotaHealthyTTL       = time.Minute
	quotaFailureTTL       = 10 * time.Second
	quotaExecutionTimeout = 5 * time.Second
)

type quotaSnapshot struct {
	usage        quotaUsage // 未折叠的独立窗口，Month 不得与展示层共享可变指针。
	label, email string
	identity     bool
}

func cloneQuotaSnapshot(s quotaSnapshot) quotaSnapshot {
	if s.usage.Month != nil {
		w := *s.usage.Month
		s.usage.Month = &w
	}
	return s
}
func (s quotaSnapshot) display() quotaUsage {
	u := cloneQuotaSnapshot(s).usage
	applyWindowHierarchy(&u)
	return u
}

type quotaFetchFunc func(context.Context, string, string, bool) (quotaSnapshot, error)
type quotaCooldownFlight struct {
	done     chan struct{}
	started  time.Time
	identity bool
	cancel   context.CancelFunc
}
type quotaCooldownEntry struct {
	snapshot  quotaSnapshot
	cached    bool
	err       error
	fetched   time.Time
	flight    *quotaCooldownFlight
	deadlines [3]time.Time
	// 已到期窗口拒绝使用到期之前开始的旧查询重新开启下一轮。
	notBefore [3]time.Time
	lastUsed  time.Time
}
type quotaCooldownManager struct {
	mu      sync.Mutex
	entries map[string]*quotaCooldownEntry
	now     func() time.Time
	fetch   quotaFetchFunc
	closed  bool
}

func newQuotaCooldownManager(fetch quotaFetchFunc) *quotaCooldownManager {
	return &quotaCooldownManager{entries: make(map[string]*quotaCooldownEntry), now: time.Now, fetch: fetch}
}
func quotaStateKey(base, key string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/") + "\x00" + authKeyHash(key)
}

// displayedQuotaExhausted 使用用户指定的整数剩余百分比，不使用截断的已用 Percent。
// 仅忽略浮点运算在 0.5% 边界的微小误差，不 trim 真实剩余余额。
func displayedQuotaExhausted(w quotaWindow) bool {
	if w.Status == "unlimited" || w.Cap <= 0 || math.IsNaN(w.Cap) || math.IsInf(w.Cap, 0) || math.IsNaN(w.Used) || math.IsInf(w.Used, 0) {
		return false
	}
	if w.Status == "exceeded" {
		return true
	}
	remaining := math.Max(0, math.Min(100, (w.Cap-w.Used)/w.Cap*100))
	return math.Round(remaining+1e-9) == 0
}
func (q *quotaCooldownManager) expireLocked(e *quotaCooldownEntry, now time.Time) {
	for i, deadline := range e.deadlines {
		if !deadline.IsZero() && !deadline.After(now) {
			e.notBefore[i] = deadline
			e.deadlines[i] = time.Time{}
			e.cached = false // 不让同一份耗尽快照在期限到点后启动整个新周期。
		}
	}
}
func (q *quotaCooldownManager) observeLocked(e *quotaCooldownEntry, s quotaSnapshot, started, anchor, now time.Time) {
	windows := [3]*quotaWindow{s.usage.Month, &s.usage.Weekly, &s.usage.FiveHour}
	selected := -1
	var deadline time.Time
	// 按月、周、5-hour 选择唯一阻塞窗口；长窗口有效期间不启动短窗口计时器。
	for i, w := range windows {
		if w == nil || !displayedQuotaExhausted(*w) || started.Before(e.notBefore[i]) {
			continue
		}
		switch i {
		case 0:
			deadline, _ = time.Parse(time.RFC3339, periodEndRFC3339(w.ResetsAt))
		case 1:
			deadline = anchor.Add(7 * 24 * time.Hour)
		case 2:
			deadline = anchor.Add(5 * time.Hour)
		}
		if deadline.After(now) {
			selected = i
			break
		}
	}
	if selected < 0 {
		return
	}
	// A live longer-window timer outranks a short-window reading. A newly
	// exhausted longer window upgrades a lower-priority timer immediately.
	for i, current := range e.deadlines {
		if !current.After(now) {
			continue
		}
		if i <= selected {
			return
		}
	}
	// Keep the original deadline for this observation round; a repeated read
	// never slides it. Drop subordinate timers when a longer one takes over.
	if e.deadlines[selected].After(now) {
		return
	}
	for i := selected + 1; i < len(e.deadlines); i++ {
		e.deadlines[i] = time.Time{}
	}
	e.deadlines[selected] = deadline
}
func (q *quotaCooldownManager) entryLocked(key string, now time.Time) *quotaCooldownEntry {
	e := q.entries[key]
	if e == nil {
		// 健康/失效凭据不会无限累积；活动冷却和在途查询不可被清理。
		for k, old := range q.entries {
			live := old.flight != nil
			for _, d := range old.deadlines {
				live = live || d.After(now)
			}
			if !live && now.Sub(old.lastUsed) > time.Hour {
				delete(q.entries, k)
			}
		}
		e = &quotaCooldownEntry{}
		q.entries[key] = e
	}
	e.lastUsed = now
	q.expireLocked(e, now)
	return e
}
func (q *quotaCooldownManager) cooldown(base, key string) *errclass.Error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	now := q.now()
	e := q.entryLocked(quotaStateKey(base, key), now)
	for i, d := range e.deadlines {
		if d.After(now) {
			names := [3]string{"monthly", "weekly", "5-hour"}
			return &errclass.Error{Class: errclass.ClassQuota, StatusCode: http.StatusTooManyRequests,
				WireCode: pluginabi.ErrorCodeCredentialQuota, CredentialScoped: true, Retryable: true,
				RetryAfterSeconds: int(math.Ceil(d.Sub(now).Seconds())),
				Message:           fmt.Sprintf("%s quota exhausted; account cooldown until %s", names[i], d.UTC().Format(time.RFC3339))}
		}
	}
	return nil
}

// load 同步执行查询，仅等待者用 channel；不创建游离 goroutine。
// force 供管理手动刷新/明确额度失败使用，after 非零则不得复用失败之前开始的查询。
// anchor 保留模型失败发生的时刻，避免补查耗时延后 5h/7d 起点。
func (q *quotaCooldownManager) load(ctx context.Context, base, key string, identity, force bool, after, anchor time.Time) (quotaSnapshot, error) {
	stateKey := quotaStateKey(base, key)
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return quotaSnapshot{}, context.Canceled
	}
	e := q.entryLocked(stateKey, q.now())
	q.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return quotaSnapshot{}, err
		}
		q.mu.Lock()
		now := q.now()
		if q.entries[stateKey] != e {
			q.mu.Unlock()
			return quotaSnapshot{}, context.Canceled
		}
		q.expireLocked(e, now)
		e.lastUsed = now
		if f := e.flight; f != nil {
			acceptable := (!identity || f.identity) && (!force || after.IsZero() || !f.started.Before(after))
			q.mu.Unlock()
			select {
			case <-ctx.Done():
				return quotaSnapshot{}, ctx.Err()
			case <-f.done:
			}
			if acceptable {
				force = false
			}
			continue
		}
		ttl := quotaHealthyTTL
		if e.err != nil {
			ttl = quotaFailureTTL
		}
		if !force && e.cached && now.Sub(e.fetched) < ttl && (!identity || e.snapshot.identity || e.err != nil) {
			s, err := cloneQuotaSnapshot(e.snapshot), e.err
			q.mu.Unlock()
			return s, err
		}
		queryCtx, cancel := context.WithCancel(ctx)
		f := &quotaCooldownFlight{done: make(chan struct{}), started: now, identity: identity, cancel: cancel}
		e.flight = f
		q.mu.Unlock()
		s, err := q.fetch(queryCtx, base, key, identity)
		cancel()
		q.mu.Lock()
		now = q.now()
		if q.entries[stateKey] != e {
			e.flight = nil
			close(f.done)
			q.mu.Unlock()
			return quotaSnapshot{}, context.Canceled
		}
		q.expireLocked(e, now)
		// 同时到达的失败可共用新鲜结果；旧查询不能跨过已到期窗口的代际边界。
		stale := false
		for _, b := range e.notBefore {
			stale = stale || f.started.Before(b)
		}
		if !stale {
			e.snapshot = cloneQuotaSnapshot(s)
			e.err = err
			e.cached = true
			e.fetched = now
			if err == nil {
				observed := now
				if !anchor.IsZero() {
					observed = anchor
				}
				q.observeLocked(e, s, f.started, observed, now)
			}
		}
		e.flight = nil
		close(f.done)
		q.mu.Unlock()
		if stale {
			force = true
			continue
		}
		return cloneQuotaSnapshot(s), err
	}
}
func (q *quotaCooldownManager) reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.clearLocked()
	q.closed = false
}
func (q *quotaCooldownManager) clearLocked() {
	for _, entry := range q.entries {
		if entry.flight != nil {
			entry.flight.cancel()
		}
	}
	q.entries = make(map[string]*quotaCooldownEntry)
}
func (q *quotaCooldownManager) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.clearLocked()
}
func (m *Manager) quotaState() *quotaCooldownManager {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.quotaCooldown == nil {
		m.quotaCooldown = newQuotaCooldownManager(func(ctx context.Context, base, key string, identity bool) (quotaSnapshot, error) {
			// 管理入口已有30秒总预算；执行入口的父ctx进一步限制为5秒。
			u, label, email, err := fetchQuotaRaw(ctx, m.bridge, base, quotaMaxTimeout, key, identity)
			return quotaSnapshot{usage: u, label: label, email: email, identity: identity}, err
		})
	}
	return m.quotaCooldown
}
func (m *Manager) observedQuota(ctx context.Context, base string, timeout time.Duration, key string) (quotaUsage, string, string, error) {
	if timeout <= 0 || timeout > quotaMaxTimeout {
		timeout = quotaMaxTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	s, err := m.quotaState().load(ctx, base, key, true, true, time.Time{}, time.Time{})
	return s.display(), s.label, s.email, err
}
func (m *Manager) preflightQuota(ctx context.Context, res *resolvedExecution) *errclass.Error {
	q := m.quotaState()
	if e := q.cooldown(res.cfg.BaseURL, res.key); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, quotaExecutionTimeout)
	defer cancel()
	_, _ = q.load(ctx, res.cfg.BaseURL, res.key, false, false, time.Time{}, time.Time{})
	return q.cooldown(res.cfg.BaseURL, res.key)
}
func (m *Manager) quotaFailure(ctx context.Context, res *resolvedExecution, original *errclass.Error) *errclass.Error {
	if original == nil || (!original.CredentialScoped && original.EnvelopeCode() != pluginabi.ErrorCodeCredentialQuota) {
		return original
	}
	q := m.quotaState()
	failed := q.now()
	ctx, cancel := context.WithTimeout(ctx, quotaExecutionTimeout)
	defer cancel()
	_, _ = q.load(ctx, res.cfg.BaseURL, res.key, false, true, failed, failed)
	if e := q.cooldown(res.cfg.BaseURL, res.key); e != nil {
		return e
	}
	return original
}
