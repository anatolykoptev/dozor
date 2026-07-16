package engine

import (
	"sync"
	"time"
)

// defaultRingCapacity is the number of alert records retained in the ring buffer.
// 100 entries cover several hours of typical alert volume on a small fleet without
// consuming significant memory (~10 KB per entry × 100 = ~1 MB worst-case).
const defaultRingCapacity = 100

// defaultRecentLimit caps the number of records returned by Recent when limit<=0.
const defaultRecentLimit = 50

// AlertRecord is a lean snapshot of an engine.Alert captured at delivery time.
// It is stored in the ring buffer so that the alerts-active MCP tool can surface
// fire-and-forget alerts (mechanical watch, monitor-script healthchecks, deploy
// failures) after they have been delivered to Telegram. Without this record the
// only evidence of those alerts is the Telegram message itself.
//
// Channel is intentionally excluded — it is internal routing metadata irrelevant
// to a caller asking "what alerts fired recently?".
type AlertRecord struct {
	Level           AlertLevel `json:"level"`
	Service         string     `json:"service"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	SuggestedAction string     `json:"suggested_action,omitempty"`
	// Timestamp is when dozor delivered this alert to Telegram (ingestion time),
	// not the alert's own start time — see Add.
	Timestamp time.Time `json:"timestamp"`
}

// AlertRing is a fixed-capacity, thread-safe circular buffer of AlertRecord values.
// Older entries are overwritten when the ring is full. The zero value is not valid;
// use NewAlertRing to construct.
//
// Design rationale: dozor's non-Prometheus alert sources (mechanical watch,
// monitor scripts, deploy failures) are fire-and-forget — once the Telegram message
// is sent the alert exists only there. The ring is the sole retained record of those
// events and is therefore cleared on process restart (acceptable; Prometheus keeps
// its own state).
type AlertRing struct {
	mu       sync.Mutex
	buf      []AlertRecord
	next     int // index of next write slot
	size     int // current number of valid entries (≤ cap(buf))
	capacity int
}

// NewAlertRing returns an AlertRing with the given capacity.
// capacity ≤ 0 uses defaultRingCapacity.
func NewAlertRing(capacity int) *AlertRing {
	if capacity <= 0 {
		capacity = defaultRingCapacity
	}
	return &AlertRing{
		buf:      make([]AlertRecord, capacity),
		capacity: capacity,
	}
}

// Add records a as delivered now, overwriting the oldest entry when the ring is full.
//
// The stored Timestamp is the INGESTION (Telegram-delivery) time, not the alert's
// own a.Timestamp. The ring is a delivery log: "recent" must mean recently delivered.
// The alertmanager path sets a.Timestamp to the alert's StartsAt (when it first began
// firing, possibly hours ago); a long-firing alert re-delivered now would otherwise
// fall outside the since-window despite just hitting Telegram. Stamping delivery time
// also keeps alerts whose source omits a timestamp (zero-value) visible.
func (r *AlertRing) Add(a Alert) {
	r.addAt(a, time.Now())
}

// addAt is the testable core of Add; at is the delivery timestamp recorded.
func (r *AlertRing) addAt(a Alert, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.buf[r.next] = AlertRecord{
		Level:           a.Level,
		Service:         a.Service,
		Title:           a.Title,
		Description:     a.Description,
		SuggestedAction: a.SuggestedAction,
		Timestamp:       at,
	}
	r.next = (r.next + 1) % r.capacity
	if r.size < r.capacity {
		r.size++
	}
}

// Recent returns records whose Timestamp >= now-since, newest-first, capped at limit.
// since ≤ 0 disables the time filter (all entries returned). limit ≤ 0 uses defaultRecentLimit.
func (r *AlertRing) Recent(since time.Duration, limit int) []AlertRecord {
	if limit <= 0 {
		limit = defaultRecentLimit
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.size == 0 {
		return nil
	}

	cutoff := time.Time{}
	if since > 0 {
		cutoff = time.Now().Add(-since)
	}

	// Walk the ring newest-first: the slot just before r.next is the most recent write.
	out := make([]AlertRecord, 0, min(r.size, limit))
	for i := range r.size {
		// Translate logical newest-first index to physical buffer index.
		idx := (r.next - 1 - i + r.capacity) % r.capacity
		rec := r.buf[idx]
		if !cutoff.IsZero() && rec.Timestamp.Before(cutoff) {
			continue
		}
		out = append(out, rec)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// DefaultAlertRing is the package-level singleton used by the alerts-active MCP
// tool and the notifyAlertFn funnel in cmd/dozor/gateway.go.
var DefaultAlertRing = NewAlertRing(defaultRingCapacity)
