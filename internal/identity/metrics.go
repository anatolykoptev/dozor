package identity

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/anatolykoptev/go-mcpserver/keyfile"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ListenerUp is 1 while the identity listener is serving and 0 when it
	// could not start or stopped on an error. The series is absent when the
	// listener is not configured.
	ListenerUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "dozor_identity_listener_up",
		Help: "1 while the identity (key-file) MCP listener is serving, 0 after it failed to start or stopped on an error.",
	})

	workerVersionMismatch = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dozor_agent_worker_version_mismatch",
		Help: "1 when the last reply from this identity's worker came from a build different from dozor's own, else 0.",
	}, []string{labelIdentity})

	agentRejects = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_agent_rejects_total",
		Help: "Agent calls answered busy without a worker result, by reason (slots, budget, backlog).",
	}, []string{labelIdentity, "reason"})

	keyfileStore  atomic.Pointer[keyfile.Store]
	keyfileGauges sync.Once
)

// ObserveKeyfile exposes dozor_mcp_keyfile_last_reload_successful for kf, read
// from the store at scrape time. go-mcpserver's keyfile does not export it
// itself. The series exists only once a store is observed.
func ObserveKeyfile(kf *keyfile.Store) {
	keyfileStore.Store(kf)
	keyfileGauges.Do(func() {
		prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "dozor_mcp_keyfile_last_reload_successful",
			Help: "1 if the last key file read succeeded, 0 while the store is denying all keys after a failed read.",
		}, func() float64 {
			if s := keyfileStore.Load(); s != nil && s.Status().LastReloadOK {
				return 1
			}
			return 0
		}))
	})
}

// ClientHooks wires the worker client's OnReply and OnReject into the
// version-mismatch gauge, the reject counter and the log. The identity is the
// verified key's run_as; the worker's build string never reaches a label.
func ClientHooks(cfg *agentsurface.Config, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	cfg.OnReply = func(id string, _ int, buildMatches bool) {
		v := 0.0
		if !buildMatches {
			v = 1
		}
		workerVersionMismatch.WithLabelValues(label(id)).Set(v)
	}
	cfg.OnReject = func(id, reason string) {
		agentRejects.WithLabelValues(label(id), reason).Inc()
		if reason == agentsurface.RejectBacklog {
			// A zero-byte reply or a reset after connect reads as busy, but a
			// worker that crashes, is OOM-killed or refuses a request also looks
			// like this. Say so, so a crash loop is not mistaken for backpressure.
			log.Warn("agent worker gave no reply (reported busy)",
				slog.String("identity", label(id)), slog.String("reason", "no-reply"))
		}
	}
}
