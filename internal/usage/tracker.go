package usage

import (
	"sort"
	"sync"

	"github.com/jonas/opencode-go-router/internal/store"
)

// Agg is a rolling-window aggregate.
type Agg struct {
	Cost     float64
	Requests int64
	Tokens   int64
}

type event struct {
	ts     int64
	cost   float64
	tokens int64
}

type keyEvents struct {
	list []event
}

// Tracker keeps a compact in-memory event history per key so limit checks are
// O(events in window) instead of a database round trip on every request.
type Tracker struct {
	mu        sync.Mutex
	maxWindow int64
	keys      map[string]*keyEvents
	total     int
}

func NewTracker(maxWindow int64) *Tracker {
	if maxWindow < 90*86400 {
		maxWindow = 90 * 86400
	}
	if maxWindow > 366*86400 {
		maxWindow = 366 * 86400
	}
	return &Tracker{maxWindow: maxWindow, keys: make(map[string]*keyEvents)}
}

func (t *Tracker) EnsureWindow(w int64) {
	if w <= 0 {
		return
	}
	if w > 366*86400 {
		w = 366 * 86400
	}
	t.mu.Lock()
	if w > t.maxWindow {
		t.maxWindow = w
	}
	t.mu.Unlock()
}

func (t *Tracker) Add(key string, ts int64, cost float64, tokens int64) {
	if key == "" || ts <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ke := t.keys[key]
	if ke == nil {
		ke = &keyEvents{}
		t.keys[key] = ke
	}
	ke.list = append(ke.list, event{ts: ts, cost: cost, tokens: tokens})
	t.total++
	// Compact occasionally to keep the backing array from growing forever.
	if len(ke.list) > 2048 {
		t.compactLocked(key, ts)
	}
}

func (t *Tracker) compactLocked(key string, now int64) {
	ke := t.keys[key]
	if ke == nil {
		return
	}
	cut := now - t.maxWindow
	idx := sort.Search(len(ke.list), func(i int) bool { return ke.list[i].ts >= cut })
	if idx == 0 {
		return
	}
	n := copy(ke.list, ke.list[idx:])
	ke.list = ke.list[:n]
	if len(ke.list) == 0 {
		delete(t.keys, key)
	}
}

// Prune discards events that are outside every configured window.
func (t *Tracker) Prune(now int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.keys {
		t.compactLocked(k, now)
	}
}

// LimitStatus is the live state of one configured limit.
type LimitStatus struct {
	ID            int64   `json:"id"`
	Metric        string  `json:"metric"`
	WindowSeconds int64   `json:"window_seconds"`
	Max           float64 `json:"max"`
	Used          float64 `json:"used"`
	ResetAt       int64   `json:"reset_at"`
}

func metricValue(a Agg, metric string) float64 {
	switch metric {
	case "requests":
		return float64(a.Requests)
	case "tokens":
		return float64(a.Tokens)
	default:
		return a.Cost
	}
}

// Check evaluates every limit for a key. The returned slice contains the live
// status of each limit; blocked is true if any limit is exhausted.
func (t *Tracker) Check(key string, limits []store.Limit, now int64) ([]LimitStatus, bool) {
	if len(limits) == 0 {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ke := t.keys[key]
	statuses := make([]LimitStatus, 0, len(limits))
	blocked := false
	for _, l := range limits {
		st := LimitStatus{ID: l.ID, Metric: l.Metric, WindowSeconds: l.WindowSeconds, Max: l.Max}
		if ke != nil {
			cut := now - l.WindowSeconds
			var agg Agg
			for i := len(ke.list) - 1; i >= 0; i-- {
				e := ke.list[i]
				if e.ts < cut {
					break
				}
				agg.Cost += e.cost
				agg.Requests++
				agg.Tokens += e.tokens
			}
			st.Used = metricValue(agg, l.Metric)
			st.ResetAt = nextReset(ke.list, cut, l, st.Used)
		}
		if st.Used+1e-9 >= l.Max {
			blocked = true
		}
		statuses = append(statuses, st)
	}
	return statuses, blocked
}

// nextReset estimates when enough old usage expires for the limit to allow a
// request again. It walks events oldest-first until the remaining usage is
// below the limit.
func nextReset(events []event, cut int64, l store.Limit, used float64) int64 {
	if len(events) == 0 || used < l.Max {
		return 0
	}
	remaining := used
	for i := 0; i < len(events); i++ {
		e := events[i]
		if e.ts < cut {
			continue
		}
		remaining -= metricValue(Agg{Cost: e.cost, Requests: 1, Tokens: e.tokens}, l.Metric)
		if remaining+1e-9 < l.Max {
			return e.ts + l.WindowSeconds
		}
	}
	return events[len(events)-1].ts + l.WindowSeconds
}
