package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// bytesPerMegabyte is the conversion factor from bytes to megabytes.
	bytesPerMegabyte = 1024 * 1024
	// dockerSocketPath is the default Docker Engine unix socket.
	dockerSocketPath = "/var/run/docker.sock"
)

// dockerSystemDF mirrors the relevant subset of Docker Engine API
// GET /system/df — https://docs.docker.com/reference/api/engine/v1.45/#tag/System
type dockerSystemDF struct {
	Images []struct {
		Size       int64 `json:"Size"`
		SharedSize int64 `json:"SharedSize"`
		Containers int   `json:"Containers"`
	} `json:"Images"`
	BuildCache []struct {
		Size  int64 `json:"Size"`
		InUse bool  `json:"InUse"`
	} `json:"BuildCache"`
	Containers []struct {
		State  string `json:"State"`
		SizeRw int64  `json:"SizeRw"`
	} `json:"Containers"`
	Volumes []struct {
		UsageData struct {
			RefCount int   `json:"RefCount"`
			Size     int64 `json:"Size"`
		} `json:"UsageData"`
	} `json:"Volumes"`
}

// scanDocker reports truly reclaimable docker disk usage in MB.
//
// Why not `docker system df`: its "Reclaimable" column sums layer sizes per
// image, which double-counts base layers shared between images — producing
// inflated estimates (e.g. 37 GB reported, only 42 MB actually recoverable).
// We query the Engine API directly and use per-image SharedSize to compute
// honest per-image unique-reclaimable (Size - SharedSize) for unused images,
// plus reclaimable build cache, stopped-container writable layers, and
// unreferenced volumes.
func (c *CleanupCollector) scanDocker(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "docker"}
	cmd := fmt.Sprintf("curl -s --unix-socket %s http://localhost/system/df", dockerSocketPath)
	res := c.transport.ExecuteUnsafe(ctx, cmd)
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		return t
	}
	mb, ok := computeDockerReclaimableMB([]byte(res.Stdout))
	if !ok {
		return t
	}
	t.Available = true
	t.SizeMB = mb
	return t
}

// computeDockerReclaimableMB parses a Docker /system/df response and returns
// actual reclaimable megabytes plus a success flag (false if JSON invalid).
// Pure function — exposed for unit testing without a live docker socket.
func computeDockerReclaimableMB(raw []byte) (float64, bool) {
	var df dockerSystemDF
	if err := json.Unmarshal(raw, &df); err != nil {
		return 0, false
	}
	var total int64
	for _, img := range df.Images {
		if img.Containers == 0 {
			total += img.Size - img.SharedSize
		}
	}
	for _, bc := range df.BuildCache {
		if !bc.InUse {
			total += bc.Size
		}
	}
	for _, cnt := range df.Containers {
		if cnt.State != "running" {
			total += cnt.SizeRw
		}
	}
	for _, vol := range df.Volumes {
		if vol.UsageData.RefCount == 0 {
			total += vol.UsageData.Size
		}
	}
	if total < 0 {
		total = 0
	}
	return float64(total) / bytesPerMegabyte, true
}

func (c *CleanupCollector) cleanDocker(ctx context.Context, minAge string) CleanupTarget {
	t := CleanupTarget{Name: "docker", Available: true}
	age := "24h"
	if minAge != "" {
		age = minAge
	}
	var freed float64
	var details []string

	res := c.transport.DockerCommand(ctx, "container prune -f --filter until="+age)
	if f := extractDockerFreed(res.Output()); f > 0 {
		freed += f
		details = append(details, fmt.Sprintf("containers: %.1f MB", f))
	}

	res = c.transport.DockerCommand(ctx, "image prune -af --filter until="+age)
	if f := extractDockerFreed(res.Output()); f > 0 {
		freed += f
		details = append(details, fmt.Sprintf("images: %.1f MB", f))
	}

	res = c.transport.DockerCommand(ctx, "builder prune -af --filter until="+age)
	if f := extractDockerFreed(res.Output()); f > 0 {
		freed += f
		details = append(details, fmt.Sprintf("build cache: %.1f MB", f))
	}

	c.transport.DockerCommand(ctx, "network prune -f")

	t.FreedMB = freed
	if len(details) > 0 {
		t.Freed = fmt.Sprintf("%.1f MB (%s)", freed, strings.Join(details, ", "))
	} else {
		t.Freed = "0.0 MB"
	}
	return t
}

// cleanDockerDangling removes dangling (untagged, unreferenced) Docker images.
// Unlike cleanDocker which uses an age filter, this targets images that are
// untagged regardless of age — these are always safe to remove.
// FreedMB prefers docker's self-reported "Total reclaimed space:" value (precise);
// falls back to df delta if docker's output is missing or zero.
// The prune runs INSIDE the measureFreedMB closure so the df window brackets
// the actual prune — making the df delta a meaningful belt-and-braces check.
func (c *CleanupCollector) cleanDockerDangling(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "docker_dangling", Available: true}
	var dockerFreedMB float64
	dfFreed := c.measureFreedMB(ctx, func() {
		res := c.transport.DockerCommand(ctx, "image prune -f --filter dangling=true")
		dockerFreedMB = extractDockerFreed(res.Output())
	})
	// Prefer docker's self-reported freed (precise); fall back to df delta
	// if docker output is missing or zero (e.g. overlay deferred coalescing).
	freed := dfFreed
	if dockerFreedMB > 0 {
		freed = dockerFreedMB
	}
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
	return t
}

// cleanDockerBuilderAged removes Docker builder cache entries older than the
// specified age (e.g. "72h"). Targets WARNING_HIGH level where builder cache
// can accumulate gigabytes from repeated image builds.
// FreedMB prefers docker's self-reported "Total reclaimed space:" value (precise);
// falls back to df delta if docker's output is missing or zero.
// The prune runs INSIDE the measureFreedMB closure so the df window brackets
// the actual prune — preventing docker self-report and df delta from being summed.
func (c *CleanupCollector) cleanDockerBuilderAged(ctx context.Context, age string) CleanupTarget {
	if age == "" {
		age = "72h"
	}
	t := CleanupTarget{Name: "docker_builder_aged", Available: true}
	var dockerFreedMB float64
	dfFreed := c.measureFreedMB(ctx, func() {
		res := c.transport.DockerCommand(ctx, "builder prune --filter until="+age+" -f")
		dockerFreedMB = extractDockerFreed(res.Output())
	})
	// Prefer docker's self-reported freed (precise); fall back to df delta
	// if docker output is missing or zero.
	freed := dfFreed
	if dockerFreedMB > 0 {
		freed = dockerFreedMB
	}
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
	return t
}

// cleanDockerBuilderAll removes ALL Docker builder cache unconditionally (no age
// filter). Last-resort target for CRITICAL/ERROR disk pressure: a single heavy
// build day can fill the docker volume with <24h cache that age-filtered prunes
// (cleanDockerBuilderAged 72h, PruneDocker until=24h) never reclaim, leaving the
// host alerting without self-healing (RCA 2026-05-25: sdc hit 91% from same-day
// cache; the filtered remediation freed 0). Mirrors cleanDockerBuilderAged's
// freed-bytes accounting (docker self-report preferred, df-delta fallback).
func (c *CleanupCollector) cleanDockerBuilderAll(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "docker_builder_all", Available: true}
	var dockerFreedMB float64
	dfFreed := c.measureFreedMB(ctx, func() {
		res := c.transport.DockerCommand(ctx, "builder prune -af")
		dockerFreedMB = extractDockerFreed(res.Output())
	})
	freed := dfFreed
	if dockerFreedMB > 0 {
		freed = dockerFreedMB
	}
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
	return t
}
