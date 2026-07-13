package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// cargoRoot is the mount point for the fleet's shared Rust build-cache volume
// (SCCACHE_BASEDIR / per-repo `target/` dirs — see ~/.cargo/config.toml).
const cargoRoot = "/mnt/cargo"

// cargoDenylist lists cargoRoot children that must never be treated as a
// per-repo build-cache target dir and pruned — the shared, cross-repo
// resources (registry/git mirrors, the sccache store, backups) that
// cleanCargo must never touch, even though they live alongside legitimate
// target dirs on the same mount.
var cargoDenylist = map[string]bool{
	"sccache-shared":        true,
	"cargo-registry-shared": true,
	"cargo-git-shared":      true,
	"backups":               true,
	"lost+found":            true,
}

// scanCargo probes cargoRoot for validated per-repo cargo target dirs and sums
// their size via du. Returns Available=false (no error) when cargoRoot is
// absent or not a mountpoint — the shared cache volume is optional infra,
// absent on hosts that don't build Rust.
func (c *CleanupCollector) scanCargo(ctx context.Context) CleanupTarget {
	t := CleanupTarget{Name: "cargo"}
	if !c.cargoMountAvailable(ctx) {
		return t
	}
	t.Available = true
	for _, dir := range c.cargoTargetDirs(ctx) {
		t.SizeMB += c.duSizeMB(ctx, dir)
	}
	return t
}

// cleanCargo age-prunes files from validated cargo target dirs under
// cargoRoot: files with access time older than minAge (a duration string like
// "4d", parsed via daysFromDuration — same convention as cleanTmp) are
// deleted, then now-empty subdirectories are removed bottom-up. Denylisted
// shared dirs and any dir lacking the cargo-target structural signature
// (debug/, release/, or .rustc_info.json) are never enumerated in the first
// place — see cargoTargetDirs. Whole-target-dir deletion is intentionally
// never used: this is a rolling age-based prune, not a wipe.
//
// .cargo-lock is excluded from deletion: it is a flock file cargo holds for
// the duration of a build — deleting it while held breaks cargo's
// single-writer serialization for that target dir.
//
// FreedMB is measured via du on cargoRoot itself (not the shared df-root
// measureFreedMB helper): cargoRoot is its own filesystem (see
// cargoMountAvailable), so a df-on-"/" bracket would always read ~0 MB freed
// here and silently defeat the freed-space metric for this target.
func (c *CleanupCollector) cleanCargo(ctx context.Context, minAge string) CleanupTarget {
	t := CleanupTarget{Name: "cargo"}
	if !c.cargoMountAvailable(ctx) {
		return t
	}
	t.Available = true
	atime := daysFromDuration(minAge)
	nice := c.niceIonicePrefix(ctx)
	dirs := c.cargoTargetDirs(ctx)
	freed := c.measureCargoFreedMB(ctx, func() {
		for _, dir := range dirs {
			c.transport.ExecuteUnsafe(ctx, fmt.Sprintf(
				"%s find '%s' -type f -atime +%s ! -name '.cargo-lock' -delete 2>/dev/null",
				nice, dir, atime))
			c.transport.ExecuteUnsafe(ctx, fmt.Sprintf(
				"find '%s' -mindepth 1 -type d -empty -delete 2>/dev/null", dir))
		}
	})
	t.FreedMB = freed
	t.Freed = fmt.Sprintf("%.1f MB", freed)
	return t
}

// cargoMountAvailable reports whether cargoRoot exists and is a mountpoint.
func (c *CleanupCollector) cargoMountAvailable(ctx context.Context) bool {
	res := c.transport.ExecuteUnsafe(ctx, "mountpoint -q "+cargoRoot)
	return res.Success
}

// niceIonicePrefix returns the best-effort I/O-throttling prefix for the find
// invocations against cargoRoot: "nice -n 19 ionice -c3" when the ionice
// binary is present, "nice -n 19" alone otherwise. ionice being missing must
// never hard-fail the prune.
func (c *CleanupCollector) niceIonicePrefix(ctx context.Context) string {
	if c.probe(ctx, "ionice") {
		return "nice -n 19 ionice -c3"
	}
	return "nice -n 19"
}

// cargoTargetDirs enumerates immediate children of cargoRoot, drops the
// denylist, and fail-closed structurally validates each remaining candidate
// via looksLikeCargoTarget — only dirs that directly contain debug/,
// release/, or .rustc_info.json are returned. This deliberately excludes
// stray non-cargo directories sharing the mount (e.g. an isolated app data
// dir) that don't follow the shared-target convention.
func (c *CleanupCollector) cargoTargetDirs(ctx context.Context) []string {
	res := c.transport.ExecuteUnsafe(ctx,
		"find "+cargoRoot+" -mindepth 1 -maxdepth 1 -type d -printf '%f\\n' 2>/dev/null")
	if !res.Success {
		return nil
	}
	var dirs []string
	for _, name := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		name = strings.TrimSpace(name)
		if name == "" || cargoDenylist[name] {
			continue
		}
		dir := cargoRoot + "/" + name
		if c.looksLikeCargoTarget(ctx, dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// looksLikeCargoTarget fail-closed validates that dir directly contains
// debug/, release/, or .rustc_info.json — the structural signature of a
// cargo build-cache target dir under the fleet's shared-target convention.
func (c *CleanupCollector) looksLikeCargoTarget(ctx context.Context, dir string) bool {
	res := c.transport.ExecuteUnsafe(ctx, fmt.Sprintf(
		"test -d '%s/debug' -o -d '%s/release' -o -f '%s/.rustc_info.json'",
		dir, dir, dir))
	return res.Success
}

// measureCargoFreedMB measures MB freed on the cargoRoot mountpoint via du
// before/after fn. See cleanCargo's doc comment for why this doesn't reuse
// the shared df-root measureFreedMB helper. Mirrors measureFreedMB's
// negative-delta guard: a concurrent writer during the window clamps the
// result to 0 rather than reporting a bogus negative "freed" value.
func (c *CleanupCollector) measureCargoFreedMB(ctx context.Context, fn func()) float64 {
	before := c.duSizeMB(ctx, cargoRoot)
	fn()
	after := c.duSizeMB(ctx, cargoRoot)
	delta := before - after
	if delta < 0 {
		slog.WarnContext(ctx, "cargo cleanup: du delta negative — another process wrote during window",
			"before_mb", before, "after_mb", after, "delta_mb", delta)
		return 0
	}
	return delta
}
