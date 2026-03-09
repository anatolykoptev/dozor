package engine

import (
	"context"
	"log/slog"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// ContainerWatcher listens for Docker container events and invalidates the cache.
type ContainerWatcher struct {
	client    *client.Client
	discovery *DockerDiscovery
	cancel    context.CancelFunc
}

// NewContainerWatcher creates a watcher that invalidates cache on container start/stop/die.
func NewContainerWatcher(cli *client.Client, discovery *DockerDiscovery) *ContainerWatcher {
	return &ContainerWatcher{
		client:    cli,
		discovery: discovery,
	}
}

// Start begins listening for container events in the background.
func (w *ContainerWatcher) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)

	f := filters.NewArgs()
	f.Add("type", string(events.ContainerEventType))

	go func() {
		slog.Info("[watcher] Docker events listener started")
		for {
			msgCh, errCh := w.client.Events(ctx, events.ListOptions{Filters: f})
		inner:
			for {
				select {
				case <-ctx.Done():
					slog.Info("[watcher] Docker events listener stopped")
					return
				case msg := <-msgCh:
					switch msg.Action {
					case events.ActionStart, events.ActionStop, events.ActionDie,
						events.ActionDestroy, events.ActionCreate:
						name := msg.Actor.Attributes["name"]
						slog.Info("[watcher] container event, cache invalidated",
							slog.String("container", name),
							slog.String("action", string(msg.Action)))
						w.discovery.InvalidateCache()
					}
				case err := <-errCh:
					if ctx.Err() != nil {
						return
					}
					slog.Info("[watcher] Docker events error, reconnecting",
						slog.String("error", err.Error()))
					break inner
				}
			}
		}
	}()
}

// Stop stops the watcher.
func (w *ContainerWatcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
}
