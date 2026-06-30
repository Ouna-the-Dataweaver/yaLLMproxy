package quota

import (
	"errors"
	"strings"
	"sync"
	"time"
)

var ErrTokenQuotaExceeded = errors.New("token quota exceeded")

const unauthenticatedKey = "__unauthenticated__"
const BackendScopeKey = "backend"

type ResetMode string

const (
	ResetModeRolling ResetMode = "rolling"
	ResetModeDaily   ResetMode = "daily"
	ResetModeWeekly  ResetMode = "weekly"
	ResetModeMonthly ResetMode = "monthly"
	ResetModeYearly  ResetMode = "yearly"
)

type WindowSpec struct {
	Window    time.Duration
	ResetMode ResetMode
}

type Manager struct {
	mu     sync.Mutex
	used   map[string]int
	window map[string]windowState
	now    func() time.Time
}

type windowState struct {
	Spec    WindowSpec
	ResetAt time.Time
}

type Reservation struct {
	manager *Manager
	scope   string
	tokens  int

	mu     sync.Mutex
	closed bool
}

type Metrics struct {
	UsedByScope    map[string]int       `json:"used_by_scope"`
	ResetAtByScope map[string]time.Time `json:"reset_at_by_scope,omitempty"`
}

func NewManager() *Manager {
	return &Manager{used: map[string]int{}, window: map[string]windowState{}, now: time.Now}
}

func NewManagerWithClock(now func() time.Time) *Manager {
	m := NewManager()
	if now != nil {
		m.now = now
	}
	return m
}

func NewManagerWithUsage(usage map[string]int) *Manager {
	m := NewManager()
	for scope, tokens := range usage {
		if tokens > 0 {
			m.used[scope] = tokens
		}
	}
	return m
}

func BuildScope(keyID string, modelName string) string {
	if keyID == "" {
		keyID = unauthenticatedKey
	}
	if modelName != "" {
		return keyID + "::" + modelName
	}
	return keyID
}

func BackendScope(backendName string) string {
	return BuildScope(BackendScopeKey, backendName)
}

func (m *Manager) Seed(keyID string, modelName string, tokens int) {
	if m == nil || tokens <= 0 {
		return
	}
	scope := BuildScope(keyID, modelName)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.used == nil {
		m.used = map[string]int{}
	}
	m.used[scope] = tokens
}

func (m *Manager) SeedWithWindowSpec(keyID string, modelName string, tokens int, spec WindowSpec) {
	if m == nil {
		return
	}
	m.SeedWithWindowSpecAt(keyID, modelName, tokens, spec, m.currentTime())
}

func (m *Manager) SeedWithWindowSpecAt(keyID string, modelName string, tokens int, spec WindowSpec, now time.Time) {
	if m == nil || tokens <= 0 {
		return
	}
	if now.IsZero() {
		now = m.currentTime()
	}
	scope := BuildScope(keyID, modelName)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.used == nil {
		m.used = map[string]int{}
	}
	if m.window == nil {
		m.window = map[string]windowState{}
	}
	m.used[scope] = tokens
	m.applyWindowLockedAt(scope, spec, now)
}

func (m *Manager) Reserve(keyID string, modelName string, limit int, tokens int) (*Reservation, error) {
	return m.ReserveWithWindow(keyID, modelName, limit, 0, tokens)
}

func (m *Manager) ReserveWithWindow(keyID string, modelName string, limit int, window time.Duration, tokens int) (*Reservation, error) {
	return m.ReserveWithWindowSpec(keyID, modelName, limit, WindowSpec{Window: window}, tokens)
}

func (m *Manager) ReserveWithWindowSpec(keyID string, modelName string, limit int, spec WindowSpec, tokens int) (*Reservation, error) {
	if m == nil || limit <= 0 || tokens <= 0 {
		return &Reservation{}, nil
	}
	scope := BuildScope(keyID, modelName)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.used == nil {
		m.used = map[string]int{}
	}
	if m.window == nil {
		m.window = map[string]windowState{}
	}
	m.applyWindowLocked(scope, spec)
	if m.used[scope]+tokens > limit {
		return nil, ErrTokenQuotaExceeded
	}
	m.used[scope] += tokens
	return &Reservation{manager: m, scope: scope, tokens: tokens}, nil
}

func (m *Manager) Remaining(keyID string, modelName string, limit int, spec WindowSpec) int {
	if limit <= 0 {
		return maxInt()
	}
	if m == nil {
		return limit
	}
	scope := BuildScope(keyID, modelName)
	m.mu.Lock()
	defer m.mu.Unlock()
	remaining := limit - m.effectiveUsedLocked(scope, spec, m.currentTime())
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (m *Manager) WouldAccept(keyID string, modelName string, limit int, spec WindowSpec, tokens int) bool {
	if m == nil || limit <= 0 || tokens <= 0 {
		return true
	}
	return m.Remaining(keyID, modelName, limit, spec) >= tokens
}

func (m *Manager) RecordUsage(keyID string, modelName string, limit int, tokens int) {
	m.RecordUsageWithWindowSpec(keyID, modelName, limit, WindowSpec{}, tokens)
}

func (m *Manager) RecordUsageWithWindowSpec(keyID string, modelName string, limit int, spec WindowSpec, tokens int) {
	if m == nil || limit <= 0 || tokens <= 0 {
		return
	}
	scope := BuildScope(keyID, modelName)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.used == nil {
		m.used = map[string]int{}
	}
	if m.window == nil {
		m.window = map[string]windowState{}
	}
	m.applyWindowLocked(scope, spec)
	m.used[scope] += tokens
}

func (m *Manager) applyWindowLocked(scope string, spec WindowSpec) {
	m.applyWindowLockedAt(scope, spec, m.currentTime())
}

func (m *Manager) effectiveUsedLocked(scope string, spec WindowSpec, now time.Time) int {
	spec = normalizeWindowSpec(spec)
	used := m.used[scope]
	if spec.Window <= 0 && spec.ResetMode == ResetModeRolling {
		return used
	}
	state := m.window[scope]
	if state.Spec != spec || state.ResetAt.IsZero() {
		return used
	}
	if !now.Before(state.ResetAt) {
		return 0
	}
	return used
}

func (m *Manager) applyWindowLockedAt(scope string, spec WindowSpec, now time.Time) {
	spec = normalizeWindowSpec(spec)
	if spec.Window <= 0 && spec.ResetMode == ResetModeRolling {
		delete(m.window, scope)
		return
	}
	state := m.window[scope]
	if state.Spec != spec || state.ResetAt.IsZero() {
		state.Spec = spec
		state.ResetAt = nextResetAt(spec, now)
		m.window[scope] = state
		return
	}
	if !now.Before(state.ResetAt) {
		m.used[scope] = 0
		if spec.ResetMode == ResetModeRolling {
			for !now.Before(state.ResetAt) {
				state.ResetAt = state.ResetAt.Add(spec.Window)
			}
		} else {
			state.ResetAt = nextResetAt(spec, now)
		}
		m.window[scope] = state
	}
}

func (r *Reservation) Commit(actualTokens int) {
	if r == nil || r.manager == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	reserved := r.tokens
	r.mu.Unlock()

	if actualTokens <= 0 || actualTokens == reserved {
		return
	}
	r.manager.mu.Lock()
	defer r.manager.mu.Unlock()
	r.manager.applyWindowLocked(r.scope, r.manager.window[r.scope].Spec)
	next := r.manager.used[r.scope] + actualTokens - reserved
	if next < 0 {
		next = 0
	}
	r.manager.used[r.scope] = next
}

func (r *Reservation) Release() {
	if r == nil || r.manager == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	tokens := r.tokens
	r.mu.Unlock()

	r.manager.mu.Lock()
	defer r.manager.mu.Unlock()
	r.manager.applyWindowLocked(r.scope, r.manager.window[r.scope].Spec)
	next := r.manager.used[r.scope] - tokens
	if next < 0 {
		next = 0
	}
	r.manager.used[r.scope] = next
}

func (m *Manager) Metrics() Metrics {
	out := Metrics{UsedByScope: map[string]int{}, ResetAtByScope: map[string]time.Time{}}
	if m == nil {
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for scope := range m.used {
		m.applyWindowLocked(scope, m.window[scope].Spec)
		out.UsedByScope[scope] = m.used[scope]
	}
	for scope, state := range m.window {
		if !state.ResetAt.IsZero() {
			out.ResetAtByScope[scope] = state.ResetAt
		}
	}
	if len(out.ResetAtByScope) == 0 {
		out.ResetAtByScope = nil
	}
	return out
}

func (m *Manager) currentTime() time.Time {
	if m != nil && m.now != nil {
		return m.now()
	}
	return time.Now()
}

func NormalizeResetMode(mode string) ResetMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", string(ResetModeRolling):
		return ResetModeRolling
	case string(ResetModeDaily):
		return ResetModeDaily
	case string(ResetModeWeekly):
		return ResetModeWeekly
	case string(ResetModeMonthly):
		return ResetModeMonthly
	case string(ResetModeYearly):
		return ResetModeYearly
	default:
		return ResetMode(strings.ToLower(strings.TrimSpace(mode)))
	}
}

func normalizeWindowSpec(spec WindowSpec) WindowSpec {
	spec.ResetMode = NormalizeResetMode(string(spec.ResetMode))
	if spec.ResetMode != ResetModeRolling {
		spec.Window = 0
	}
	return spec
}

func nextResetAt(spec WindowSpec, now time.Time) time.Time {
	if spec.ResetMode == ResetModeRolling {
		return now.Add(spec.Window)
	}
	return NextResetAt(spec.ResetMode, now)
}

func NextResetAt(mode ResetMode, now time.Time) time.Time {
	utc := now.UTC()
	switch NormalizeResetMode(string(mode)) {
	case ResetModeDaily:
		return time.Date(utc.Year(), utc.Month(), utc.Day()+1, 0, 0, 0, 0, time.UTC)
	case ResetModeWeekly:
		daysUntilMonday := (int(time.Monday) - int(utc.Weekday()) + 7) % 7
		if daysUntilMonday == 0 {
			daysUntilMonday = 7
		}
		next := utc.AddDate(0, 0, daysUntilMonday)
		return time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, time.UTC)
	case ResetModeMonthly:
		return time.Date(utc.Year(), utc.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	case ResetModeYearly:
		return time.Date(utc.Year()+1, time.January, 1, 0, 0, 0, 0, time.UTC)
	default:
		return time.Time{}
	}
}

func WindowStartAt(mode ResetMode, now time.Time) time.Time {
	utc := now.UTC()
	switch NormalizeResetMode(string(mode)) {
	case ResetModeDaily:
		return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	case ResetModeWeekly:
		daysSinceMonday := (int(utc.Weekday()) - int(time.Monday) + 7) % 7
		start := utc.AddDate(0, 0, -daysSinceMonday)
		return time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	case ResetModeMonthly:
		return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
	case ResetModeYearly:
		return time.Date(utc.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
	default:
		return time.Time{}
	}
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
