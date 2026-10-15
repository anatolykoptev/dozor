package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/anatolykoptev/dozor/internal/deploy"
	kitratelimit "github.com/anatolykoptev/go-kit/ratelimit"
)

// Rate limit for the public POST /deploy/github route (issue #262). It exists to
// cap unauthenticated abuse (every request costs a body read of up to 5 MiB plus
// an HMAC) - NOT to throttle GitHub. GitHub does not retry a 429, so a dropped
// delivery is a silently missed deploy. ~20 repos post push + release webhooks,
// and several can merge at once: a burst of 60 covers all of them with margin,
// and 2/s sustained (120/min) refills it within half a minute. The key is the
// client IP resolved by sourceKey (X-Forwarded-For from the loopback proxy), so
// an unauthenticated flood from one address cannot spend another's tokens.
const (
	deployWebhookRate  = 2.0
	deployWebhookBurst = 60
)

// deployWebhookLimiter is a separate bucket set from webhookLimiter so a
// deploy burst cannot starve /webhook* monitors and vice versa.
var deployWebhookLimiter = kitratelimit.NewKeyLimiter(deployWebhookRate, deployWebhookBurst)

var deployLimitedWarn warnGate

func init() {
	deployWebhookLimiter.StartCleanup(10*time.Minute, 30*time.Minute)
}

// mountDeployWebhook mounts POST /deploy/github behind the rate limit. The
// limit runs before the handler, i.e. before the body read and HMAC.
func mountDeployWebhook(mx *http.ServeMux, h http.Handler) {
	mx.Handle("POST /deploy/github", rateLimitDeployWebhook(h))
}

func rateLimitDeployWebhook(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := webhookSourceKey(r)
		if !deployWebhookLimiter.Allow(key) {
			deploy.WebhookRejectedTotal.WithLabelValues("rate_limited").Inc()
			// One log line per request would let a flood fill the journal; the
			// metric carries the count.
			if deployLimitedWarn.allow() {
				slog.Warn("deploy/webhook: rate limited (sampled: at most once a minute)",
					slog.String("client_ip", key))
			}
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
