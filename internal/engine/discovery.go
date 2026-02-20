package engine

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// ServiceCache caches discovered service names with TTL.
type ServiceCache struct {
	services  []string
	fetchedAt time.Time
	ttl       time.Duration
	mu        sync.RWMutex
}

// Get returns cached services if still valid.
func (c *ServiceCache) Get() ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.services == nil || time.Since(c.fetchedAt) > c.ttl {
		return nil, false
	}
	return c.services, true
}

// Set stores services in cache.
func (c *ServiceCache) Set(services []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.services = services
	c.fetchedAt = time.Now()
}

// Invalidate clears the cache.
func (c *ServiceCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.services = nil
}

// DiscoveredContainer holds info from Docker SDK inspection.
type DiscoveredContainer struct {
	ID      string
	Name    string            // cleaned container name (no leading /)
	Service string            // compose service name if available
	State   ContainerState
	Uptime  string
	Labels  map[string]string
}

// DozorLabel returns a dozor-specific label value.
func (d DiscoveredContainer) DozorLabel(key string) string {
	return d.Labels["dozor."+key]
}

// IsEnabled checks dozor.enable label. Default: true (opt-out model).
func (d DiscoveredContainer) IsEnabled() bool {
	return d.DozorLabel("enable") != "false"
}

// DisplayName returns dozor.name label or the service/container name.
func (d DiscoveredContainer) DisplayName() string {
	if n := d.DozorLabel("name"); n != "" {
		return n
	}
	if d.Service != "" {
		return d.Service
	}
	return d.Name
}

// DockerDiscovery uses Docker SDK for container discovery and inspection.
type DockerDiscovery struct {
	client *client.Client
	cache  *ServiceCache
}

// NewDockerDiscovery creates SDK-based discovery. Returns nil if Docker is unavailable.
func NewDockerDiscovery() *DockerDiscovery {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Printf("[discovery] Docker SDK init failed: %v", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		log.Printf("[discovery] Docker not reachable: %v", err)
		cli.Close()
		return nil
	}
	return &DockerDiscovery{
		client: cli,
		cache:  &ServiceCache{ttl: 30 * time.Second},
	}
}

// Close releases the Docker client.
func (d *DockerDiscovery) Close() {
	if d != nil && d.client != nil {
		d.client.Close()
	}
}

// Client returns the underlying Docker client for use by the watcher.
func (d *DockerDiscovery) Client() *client.Client {
	return d.client
}

// DiscoverServices returns service names of all containers.
// Respects dozor.enable label (opt-out). Results are cached for 30s.
func (d *DockerDiscovery) DiscoverServices(ctx context.Context) []string {
	if cached, ok := d.cache.Get(); ok {
		return cached
	}

	containers := d.ListContainers(ctx)
	names := make([]string, 0, len(containers))
	for _, c := range containers {
		if !c.IsEnabled() {
			continue
		}
		names = append(names, c.DisplayName())
	}
	d.cache.Set(names)
	return names
}

// ListContainers returns all containers with metadata via SDK.
func (d *DockerDiscovery) ListContainers(ctx context.Context) []DiscoveredContainer {
	containers, err := d.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		log.Printf("[discovery] ContainerList failed: %v", err)
		return nil
	}

	result := make([]DiscoveredContainer, 0, len(containers))
	for _, c := range containers {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		dc := DiscoveredContainer{
			ID:      c.ID[:12],
			Name:    name,
			Service: c.Labels["com.docker.compose.service"],
			State:   parseContainerState(c.State),
			Labels:  c.Labels,
		}
		if strings.HasPrefix(c.Status, "Up ") {
			dc.Uptime = c.Status[3:]
		}
		result = append(result, dc)
	}
	return result
}

// InspectContainer returns detailed status for a single container via SDK.
func (d *DockerDiscovery) InspectContainer(ctx context.Context, nameOrID string) (ServiceStatus, bool) {
	// Find by name filter first
	f := filters.NewArgs()
	f.Add("name", nameOrID)
	containers, err := d.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		return ServiceStatus{Name: nameOrID, State: StateUnknown}, false
	}

	// Find best match (filter returns partial matches too)
	var target *container.Summary
	for i := range containers {
		c := &containers[i]
		cName := ""
		if len(c.Names) > 0 {
			cName = strings.TrimPrefix(c.Names[0], "/")
		}
		svcName := c.Labels["com.docker.compose.service"]
		if cName == nameOrID || svcName == nameOrID || strings.Contains(cName, nameOrID) {
			target = c
			break
		}
	}
	if target == nil {
		return ServiceStatus{Name: nameOrID, State: StateUnknown}, false
	}

	// Full inspect for restart count and health
	inspect, err := d.client.ContainerInspect(ctx, target.ID)
	if err != nil {
		status := ServiceStatus{
			Name:  nameOrID,
			State: parseContainerState(target.State),
		}
		if strings.HasPrefix(target.Status, "Up ") {
			status.Uptime = target.Status[3:]
		}
		return status, true
	}

	status := ServiceStatus{
		Name:         nameOrID,
		State:        parseContainerState(inspect.State.Status),
		RestartCount: inspect.RestartCount,
		Labels:       target.Labels,
	}
	if strings.HasPrefix(target.Status, "Up ") {
		status.Uptime = target.Status[3:]
	}
	if inspect.State.Health != nil {
		status.Health = inspect.State.Health.Status
	}

	return status, true
}

// InvalidateCache forces re-discovery on next call.
func (d *DockerDiscovery) InvalidateCache() {
	d.cache.Invalidate()
}
