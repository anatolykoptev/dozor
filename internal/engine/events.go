package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// dockerEventsClient is the subset of docker client used by RestartsInWindow.
// Defined at the consumer so tests can mock it.
type dockerEventsClient interface {
	Events(ctx context.Context, opts events.ListOptions) (<-chan events.Message, <-chan error)
}

// RestartsInWindow counts how many times a container has died (restarted)
// within the given time window by querying Docker Events API.
// Returns 0 on any error so callers can treat it as a safe fallback.
func RestartsInWindow(ctx context.Context, cli dockerEventsClient, containerName string, window time.Duration) int {
	now := time.Now()
	since := now.Add(-window)

	f := filters.NewArgs()
	f.Add("type", string(events.ContainerEventType))
	f.Add("event", string(events.ActionDie))
	f.Add("container", containerName)

	queryCtx, cancel := context.WithTimeout(ctx, dockerPingTimeoutSec*time.Second)
	defer cancel()

	// Until bounded — without it the stream keeps waiting for future events
	// and the call blocks until context timeout.
	msgCh, errCh := cli.Events(queryCtx, events.ListOptions{
		Since:   since.Format(time.RFC3339Nano),
		Until:   now.Format(time.RFC3339Nano),
		Filters: f,
	})

	count := 0
	for {
		select {
		case msg, ok := <-msgCh:
			if !ok {
				// Channel closed — all historical events have been delivered.
				return count
			}
			// Only count events after our since boundary (Docker may include border events).
			eventTime := time.Unix(msg.Time, 0)
			if eventTime.After(since) {
				count++
			}
		case err, ok := <-errCh:
			if !ok {
				// errCh closed without an error — normal end of historical stream.
				// Keep draining msgCh until it closes too.
				errCh = nil
				continue
			}
			if queryCtx.Err() == nil {
				// Unexpected error (not context cancellation).
				slog.Warn("[events] RestartsInWindow error", slog.String("container", containerName), slog.String("err", err.Error()))
			}
			return count
		case <-queryCtx.Done():
			return count
		}
	}
}

// restartsInWindow wraps RestartsInWindow using the production *client.Client.
// Defined separately so discovery.go can call it without importing events package.
func restartsInWindow(ctx context.Context, cli *client.Client, containerName string, window time.Duration) int {
	return RestartsInWindow(ctx, cli, containerName, window)
}
