package admission

import (
	"container/heap"
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrQueueTimeout       = errors.New("concurrency queue timeout")
	ErrClientDisconnected = errors.New("client disconnected while queued")
	ErrRateLimitExceeded  = errors.New("rate limit exceeded")
)

const UnauthenticatedKey = "__unauthenticated__"

type Manager struct {
	mu      sync.Mutex
	states  map[string]*state
	waiting priorityQueue
	seq     int64
	now     func() time.Time
}

type state struct {
	scope            string
	concurrencyLimit int
	priority         int
	active           map[int64]struct{}
	totalRequests    int
	totalQueued      int
	totalWait        time.Duration
	maxQueueDepth    int
	rateLimited      int
	rpsBucket        tokenBucket
	rpmBucket        tokenBucket
}

type RateLimits struct {
	RPS float64
	RPM int
}

type queuedRequest struct {
	priority  int
	seq       int64
	scope     string
	ready     chan struct{}
	cancelled bool
	index     int
}

type Slot struct {
	manager *Manager
	scope   string
	id      int64
	Wait    time.Duration

	mu       sync.Mutex
	released bool
}

type Metrics struct {
	GlobalQueueDepth int
	ActiveByScope    map[string]int
	QueuedByScope    map[string]int
	State            map[string]ScopeMetrics
}

type ScopeMetrics struct {
	ConcurrencyLimit int
	Priority         int
	ActiveCount      int
	TotalRequests    int
	TotalQueued      int
	MaxQueueDepth    int
	AverageWait      time.Duration
	RateLimited      int
	RPSTokens        float64
	RPMTokens        float64
}

func NewManager() *Manager {
	return &Manager{states: map[string]*state{}, now: time.Now}
}

func BuildScope(keyID string, modelName string) string {
	if keyID == "" {
		keyID = UnauthenticatedKey
	}
	if modelName != "" {
		return keyID + "::" + modelName
	}
	return keyID
}

func (m *Manager) Acquire(ctx context.Context, keyID string, concurrencyLimit int, priority int, modelName string) (*Slot, error) {
	if m == nil {
		m = NewManager()
	}
	scope := BuildScope(keyID, modelName)
	start := time.Now()

	m.mu.Lock()
	m.seq++
	id := m.seq
	st := m.getState(scope, concurrencyLimit, priority)
	st.totalRequests++
	if st.hasAvailableSlot() {
		st.active[id] = struct{}{}
		m.mu.Unlock()
		return &Slot{manager: m, scope: scope, id: id}, nil
	}
	q := &queuedRequest{
		priority: priority,
		seq:      id,
		scope:    scope,
		ready:    make(chan struct{}),
	}
	heap.Push(&m.waiting, q)
	st.totalQueued++
	depth := m.queueDepthLocked(scope)
	if depth > st.maxQueueDepth {
		st.maxQueueDepth = depth
	}
	m.mu.Unlock()

	select {
	case <-ctx.Done():
		m.cancel(q)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrQueueTimeout
		}
		return nil, ErrClientDisconnected
	case <-q.ready:
		wait := time.Since(start)
		m.mu.Lock()
		if st := m.states[scope]; st != nil {
			st.totalWait += wait
		}
		m.mu.Unlock()
		return &Slot{manager: m, scope: scope, id: id, Wait: wait}, nil
	}
}

func (m *Manager) CheckRateLimit(keyID string, modelName string, limits RateLimits) error {
	if m == nil {
		return nil
	}
	if limits.RPS <= 0 && limits.RPM <= 0 {
		return nil
	}
	scope := BuildScope(keyID, modelName)
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.states[scope]
	if st == nil {
		st = &state{scope: scope, active: map[int64]struct{}{}}
		m.states[scope] = st
	}
	now := time.Now()
	if m.now != nil {
		now = m.now()
	}
	if !st.allowRate(now, limits) {
		st.rateLimited++
		return ErrRateLimitExceeded
	}
	return nil
}

func (m *Manager) RateLimitAvailable(keyID string, modelName string, limits RateLimits) bool {
	if m == nil {
		return true
	}
	if limits.RPS <= 0 && limits.RPM <= 0 {
		return true
	}
	scope := BuildScope(keyID, modelName)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if m.now != nil {
		now = m.now()
	}
	st := m.states[scope]
	if st == nil {
		st = &state{}
	}
	return st.rateAvailable(now, limits)
}

func (m *Manager) getState(scope string, concurrencyLimit int, priority int) *state {
	st := m.states[scope]
	if st == nil {
		st = &state{scope: scope, active: map[int64]struct{}{}}
		m.states[scope] = st
	}
	st.concurrencyLimit = concurrencyLimit
	st.priority = priority
	return st
}

func (s *state) hasAvailableSlot() bool {
	return s.concurrencyLimit <= 0 || len(s.active) < s.concurrencyLimit
}

func (s *state) allowRate(now time.Time, limits RateLimits) bool {
	if limits.RPS > 0 {
		s.rpsBucket.configure(limits.RPS, limits.RPS)
		s.rpsBucket.refill(now)
	}
	if limits.RPM > 0 {
		capacity := float64(limits.RPM)
		s.rpmBucket.configure(capacity, capacity/60.0)
		s.rpmBucket.refill(now)
	}
	if limits.RPS > 0 && s.rpsBucket.tokens < 1 {
		return false
	}
	if limits.RPM > 0 && s.rpmBucket.tokens < 1 {
		return false
	}
	if limits.RPS > 0 {
		s.rpsBucket.tokens--
	}
	if limits.RPM > 0 {
		s.rpmBucket.tokens--
	}
	return true
}

func (s *state) rateAvailable(now time.Time, limits RateLimits) bool {
	rpsBucket := s.rpsBucket
	rpmBucket := s.rpmBucket
	if limits.RPS > 0 {
		rpsBucket.configure(limits.RPS, limits.RPS)
		rpsBucket.refill(now)
	}
	if limits.RPM > 0 {
		capacity := float64(limits.RPM)
		rpmBucket.configure(capacity, capacity/60.0)
		rpmBucket.refill(now)
	}
	if limits.RPS > 0 && rpsBucket.tokens < 1 {
		return false
	}
	if limits.RPM > 0 && rpmBucket.tokens < 1 {
		return false
	}
	return true
}

func (s *Slot) Release() {
	if s == nil || s.manager == nil {
		return
	}
	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return
	}
	s.released = true
	s.mu.Unlock()
	s.manager.release(s.scope, s.id)
}

func (m *Manager) release(scope string, id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.states[scope]
	if st == nil {
		return
	}
	delete(st.active, id)
	m.signalNextLocked(scope, st)
}

func (m *Manager) signalNextLocked(scope string, st *state) {
	if !st.hasAvailableSlot() {
		return
	}
	var selected *queuedRequest
	var selectedIndex = -1
	for i, q := range m.waiting {
		if q.cancelled || q.scope != scope {
			continue
		}
		if selected == nil || q.priority < selected.priority || (q.priority == selected.priority && q.seq < selected.seq) {
			selected = q
			selectedIndex = i
		}
	}
	if selected == nil {
		return
	}
	heap.Remove(&m.waiting, selectedIndex)
	st.active[selected.seq] = struct{}{}
	close(selected.ready)
}

func (m *Manager) cancel(q *queuedRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if q.cancelled {
		return
	}
	q.cancelled = true
	if q.index >= 0 && q.index < len(m.waiting) && m.waiting[q.index] == q {
		heap.Remove(&m.waiting, q.index)
	}
}

func (m *Manager) queueDepthLocked(scope string) int {
	depth := 0
	for _, q := range m.waiting {
		if !q.cancelled && q.scope == scope {
			depth++
		}
	}
	return depth
}

func (m *Manager) Metrics() Metrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	metrics := Metrics{
		ActiveByScope: map[string]int{},
		QueuedByScope: map[string]int{},
		State:         map[string]ScopeMetrics{},
	}
	for _, q := range m.waiting {
		if !q.cancelled {
			metrics.GlobalQueueDepth++
			metrics.QueuedByScope[q.scope]++
		}
	}
	for scope, st := range m.states {
		active := len(st.active)
		metrics.ActiveByScope[scope] = active
		avg := time.Duration(0)
		if st.totalQueued > 0 {
			avg = st.totalWait / time.Duration(st.totalQueued)
		}
		metrics.State[scope] = ScopeMetrics{
			ConcurrencyLimit: st.concurrencyLimit,
			Priority:         st.priority,
			ActiveCount:      active,
			TotalRequests:    st.totalRequests,
			TotalQueued:      st.totalQueued,
			MaxQueueDepth:    st.maxQueueDepth,
			AverageWait:      avg,
			RateLimited:      st.rateLimited,
			RPSTokens:        st.rpsBucket.tokens,
			RPMTokens:        st.rpmBucket.tokens,
		}
	}
	return metrics
}

type tokenBucket struct {
	capacity        float64
	tokens          float64
	refillPerSecond float64
	lastRefill      time.Time
}

func (b *tokenBucket) configure(capacity float64, refillPerSecond float64) {
	if capacity <= 0 || refillPerSecond <= 0 {
		*b = tokenBucket{}
		return
	}
	if b.capacity != capacity || b.refillPerSecond != refillPerSecond {
		b.capacity = capacity
		b.refillPerSecond = refillPerSecond
		if b.tokens <= 0 || b.tokens > capacity {
			b.tokens = capacity
		}
	}
}

func (b *tokenBucket) refill(now time.Time) {
	if b.capacity <= 0 || b.refillPerSecond <= 0 {
		return
	}
	if b.lastRefill.IsZero() || now.Before(b.lastRefill) {
		b.lastRefill = now
		if b.tokens <= 0 {
			b.tokens = b.capacity
		}
		return
	}
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	b.tokens += elapsed * b.refillPerSecond
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.lastRefill = now
}

type priorityQueue []*queuedRequest

func (p priorityQueue) Len() int { return len(p) }
func (p priorityQueue) Less(i, j int) bool {
	if p[i].priority == p[j].priority {
		return p[i].seq < p[j].seq
	}
	return p[i].priority < p[j].priority
}
func (p priorityQueue) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
	p[i].index = i
	p[j].index = j
}
func (p *priorityQueue) Push(x any) {
	q := x.(*queuedRequest)
	q.index = len(*p)
	*p = append(*p, q)
}
func (p *priorityQueue) Pop() any {
	old := *p
	n := len(old)
	q := old[n-1]
	q.index = -1
	*p = old[:n-1]
	return q
}
