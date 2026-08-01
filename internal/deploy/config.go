// Package deploy implements GitHub webhook-driven service rebuilds.
// Supports three deploy kinds:
//   - "compose" (default): docker compose build + up
//   - "binary": git pull + go build + systemctl --user restart
//   - "static": run a custom deploy script (Astro / Vite / Next static export)
package deploy

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration to support YAML unmarshaling from strings like "30s".
type Duration struct{ D time.Duration }

// UnmarshalYAML implements yaml.Unmarshaler so "30s" in YAML becomes 30*time.Second.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	dur, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", value.Value, err)
	}
	d.D = dur
	return nil
}

// OrDefault returns d.D if non-zero, otherwise the fallback.
func (d Duration) OrDefault(fallback time.Duration) time.Duration {
	if d.D <= 0 {
		return fallback
	}
	return d.D
}

// DeployKind selects the build strategy for a repository.
type DeployKind string

const (
	KindCompose DeployKind = "compose" // Docker Compose (default)
	KindBinary  DeployKind = "binary"  // native Go binary + systemd user service
	KindStatic  DeployKind = "static"  // custom deploy script (Astro / Vite / Next static export)
)

// deploy_on accepted values (see RepoConfig.DeployOn). Centralised so the
// webhook router, drift checker, and gate share one source of truth.
const (
	deployOnRelease = "release"
	deployOnManual  = "manual"
)

// RepoConfig maps a GitHub repository to its deploy strategy.
type RepoConfig struct {
	// Kind selects the deploy strategy. Default: "compose".
	Kind DeployKind `yaml:"kind,omitempty"`

	// Branch is the git branch that triggers deploys. Default: "main".
	// Use this for repos that deploy from a non-standard branch (e.g. "master").
	Branch string `yaml:"branch,omitempty"`

	// --- compose fields ---
	ComposePath string `yaml:"compose_path,omitempty"`
	NoCache     bool   `yaml:"no_cache,omitempty"`

	// --- shared / binary fields ---
	SourcePath string   `yaml:"source_path"`
	Services   []string `yaml:"services,omitempty"` // docker compose service names

	// --- binary-only fields ---
	// BuildCmd is the command to build the binary, e.g. ["go", "build", "-o", "/path/bin", "./cmd/svc"].
	// Runs in SourcePath.
	BuildCmd []string `yaml:"build_cmd,omitempty"`
	// UserServices lists systemd user-service names to restart after a successful build.
	// The first entry is the canary: it is restarted and smoke-tested before the rest.
	UserServices []string `yaml:"user_services,omitempty"`

	// SmokeURL is probed after deploy — must return 2xx within smokeTimeout.
	SmokeURL string `yaml:"smoke_url,omitempty"`

	// CanarySmokeTimeout is how long to wait for the canary smoke_url to return 200
	// for the first time. Default: 30s.
	CanarySmokeTimeout Duration `yaml:"canary_smoke_timeout,omitempty"`

	// CanarySmokeWindow is how long smoke_url must sustain 200 responses (polled
	// every 5s) after the initial hit before the remaining services are restarted.
	// Default: 30s.
	CanarySmokeWindow Duration `yaml:"canary_smoke_window,omitempty"`

	// BuildPaths is a whitelist of glob patterns. If non-empty, the webhook
	// triggers a rebuild only when at least one changed file matches one of
	// these patterns. Empty (the default) preserves backward-compat behaviour:
	// every push to main rebuilds.
	//
	// Globbing is minimal-doublestar (see path_filter.go):
	//   "memdb-go/**"  matches every file under memdb-go/
	//   "*.md"         matches a top-level *.md (does not cross '/')
	//   "**/*.md"      matches *.md at any depth
	//   "go.mod"       matches the literal file
	BuildPaths []string `yaml:"build_paths,omitempty"`

	// SkipPaths is a deny-list of glob patterns applied BEFORE the BuildPaths
	// whitelist check. Files matching SkipPaths are subtracted from the changed
	// set: even if such a file would otherwise match BuildPaths, it does not
	// count toward "deploy-worthy" change. Useful when an operator wants a
	// permissive BuildPaths (e.g. `**/*`) but explicit exclusion of certain
	// directories (e.g. `tmp/**`, `target/**`, `docs/**`).
	//
	// Empty (the default) preserves backward-compat: no deny filtering.
	// When the entire changed set is consumed by SkipPaths, the push is
	// skipped with reason="only_skip_paths".
	SkipPaths []string `yaml:"skip_paths,omitempty"`

	// SkipIfAny is a list of glob patterns that, if ANY changed file matches,
	// causes the entire build to be skipped for this repo entry. Unlike
	// SkipPaths (which subtracts individual files from the changed set before
	// the BuildPaths check), SkipIfAny is a hard veto: a single matching file
	// aborts the build regardless of other matching files.
	//
	// Use case: a web-only deploy lane that should fire only when the diff is
	// purely web/frontend files. Set SkipIfAny to full-lane trigger paths
	// (crates/**, Cargo.toml, Dockerfile, …) so a push that touches both
	// web/** and crates/** skips the web-only lane and falls through to the
	// full lane instead.
	//
	// Skip reason: "skip_if_any". Checked before BuildPaths/SkipPaths.
	// Empty (the default) preserves backward-compat: no hard veto.
	SkipIfAny []string `yaml:"skip_if_any,omitempty"`

	// DeployOn gates which GitHub event triggers a build for this repo.
	//   - "" (default): every push to the configured branch builds — the
	//     original push-based behaviour, unchanged.
	//   - "release": the repo builds ONLY on a GitHub "release published"
	//     event (a release-please release PR merge), NOT on every push.
	//     Use for heavy services (Rust workspaces etc.) where work accrues
	//     on main between releases and rebuilding on every push wastes the
	//     build host. The release-event path (webhook_release.go) handles
	//     it exactly as any other repo — no special-casing there.
	//   - "manual": the repo is triggered by the SAME release event as
	//     "release" (so its webhook still needs the "release" subscription),
	//     but on that event dozor does everything it does today EXCEPT the
	//     deploy: it resolves the target, builds/pulls the image (build-once-
	//     promote lets it reuse the canary's published tree-hash image), and
	//     STOPS before bringing containers up. Deployment happens only via
	//     the explicit server_deploy path (ExecuteManualDeploy). A pending-
	//     deploy gauge (dozor_pending_deploy) is set per service so a
	//     released-but-never-deployed fix is visible instead of silent. Use
	//     for production targets that must never deploy automatically — the
	//     inverse of the opt-out no-auto-deploy label (issue #183).
	//
	// Any other value is rejected at config load with an error naming the
	// repo and the bad value.
	DeployOn string `yaml:"deploy_on,omitempty"`

	// Profile selects a built-in preset for BuildPaths/SkipPaths. Known values:
	// "go-flat", "go-cmd", "rust". Empty (default) means no preset.
	Profile string `yaml:"profile,omitempty"`

	// BuildPathsExtra is appended to the profile's default build_paths.
	// Meaningless (and a load-time error) without Profile set.
	BuildPathsExtra []string `yaml:"build_paths_extra,omitempty"`

	// SkipPathsExtra is appended to the profile's default skip_paths.
	// Meaningless (and a load-time error) without Profile set.
	SkipPathsExtra []string `yaml:"skip_paths_extra,omitempty"`

	// DebounceSeconds coalesces a burst of webhooks for the same repo+service
	// into a single rebuild dispatched after this many seconds of silence
	// from the last event. 0 (default) disables debouncing.
	DebounceSeconds int `yaml:"debounce_seconds,omitempty"`

	// --- static-only fields ---

	// StaticDeployScript (kind=static): absolute path to a bash script that
	// performs the atomic deploy. The script receives two environment variables:
	//   DEPLOY_REPO_PATH  — absolute path to the local git checkout (SourcePath)
	//   DEPLOY_SHA        — commit SHA from the webhook
	// stdout+stderr are captured and logged. A non-zero exit code is a failure.
	StaticDeployScript string `yaml:"static_deploy_script,omitempty"`

	// PruneBuildkitCache, when true, runs
	// `docker buildx prune --force --filter type=exec.cachemount`
	// before each `docker compose build` for this repo.
	//
	// Background: BuildKit cache mounts (--mount=type=cache,target=...) persist
	// between builds even with --no-cache — that flag only invalidates layer
	// cache, not exec cache mounts. For Rust services the cargo target/ lives in
	// a cache mount; stale binaries survive into the new image. Set true for any
	// Rust service whose Dockerfile uses --mount=type=cache on target/.
	//
	// Default: false. Prune cost is real (~6-8 min cold rebuild); only enable
	// where the silent-stale-binary risk is confirmed.
	PruneBuildkitCache bool `yaml:"prune_buildkit_cache,omitempty"`

	// BuildTimeout overrides the global 15-minute build timeout for this repo.
	// Accepts Go duration strings: "15m", "45m", "1h". Must be positive.
	// Default (zero value): uses the package-level buildTimeout constant (15m).
	//
	// Increase for repos with slow builds (e.g. cold-cache Rust workspace with
	// no_cache: true). Example: build_timeout: 45m
	BuildTimeout Duration `yaml:"build_timeout,omitempty"`

	// Heavy marks this repo as a resource-intensive build that acquires the
	// global heavy-build semaphore (in addition to the per-slot concurrency
	// limit). At most one heavy build runs at a time regardless of
	// DOZOR_BUILD_CONCURRENCY, preventing OOM on the ARM build host when
	// concurrent Rust/Docker builds are enabled.
	//
	// Set heavy: true for repos with no_cache:true Rust builds or multi-stage
	// Docker builds that pin >4 GB RAM during compile.
	Heavy bool `yaml:"heavy,omitempty"`

	// IgnoreNoAutoDeployLabel, when true, bypasses the no-auto-deploy PR label
	// and commit message marker check for this repo. Use when all merges must
	// deploy regardless of PR labels (e.g. a repo where the label has a
	// different meaning in the review workflow).
	IgnoreNoAutoDeployLabel bool `yaml:"ignore_no_auto_deploy_label,omitempty"`

	// DeployClonePath is the absolute path to the deploy clone whose
	// docker-compose files dozor reads (compose_path lives here).
	// When set, dozor auto-pulls this clone to origin/<branch> before every
	// build, ensuring the compose config is never stale.
	//
	// If the clone is dirty (uncommitted local edits) the pull is skipped with
	// a WARN log and the build proceeds with the current state — operator is
	// notified via the deploy_clone_pull_total{outcome="dirty_skipped"} counter.
	//
	// If --ff-only pull fails (e.g. diverged) the pull is skipped with a WARN
	// log; the build proceeds with the current state.
	//
	// If omitted, no auto-pull is performed (backward-compatible default).
	//
	// Example (krolik-server deploy clone):
	//   deploy_clone_path: /home/krolik/deploy/krolik-server
	DeployClonePath string `yaml:"deploy_clone_path,omitempty"`

	// ImageCache enables build-once-promote: the production image is built
	// once, tagged by git tree hash, pushed to a registry, and pulled
	// (instead of rebuilt) by the next deploy of the same tree. Opt-in per
	// repo; absent (zero value) on all other repos — default OFF.
	//
	// The tree hash (git rev-parse <commit>^{tree}) is the same for two
	// commits that produce identical source trees (e.g. a dev→main merge
	// whose merge commit has a new SHA but the same tree as dev's HEAD).
	// Tagging by tree hash lets prod reuse the artifact stagingprod already
	// validated, eliminating the duplicate build and the canary-ships-a-
	// different-artifact class proven by the v0.15.0 incident.
	//
	// Pull failure (image not found, registry down, auth failure, timeout)
	// ALWAYS falls through to the existing build path — the change is never
	// worse than the status quo. Push failure is best-effort (never fails
	// the deploy) but logs at ERROR level so a silently-failing push is
	// observable.
	ImageCache ImageCacheConfig `yaml:"image_cache,omitempty"`

	// BuildArgs is a list of extra --build-arg values passed to
	// `docker compose build` for this repo. Each entry is a "KEY=VALUE"
	// string. The placeholder ${SHA} is substituted with the 12-char
	// short commit SHA (matching the image tag format used by
	// deploy-web-only.sh: SHORT_SHA="${SHA:0:12}").
	//
	// Use case: passing a pre-built web OCI artifact image tag to a
	// multi-stage Dockerfile that supports consuming it via
	// `ARG WEB_ARTIFACT_IMAGE` + `FROM ${WEB_ARTIFACT_IMAGE} AS web-source`,
	// so the runtime image reuses the exact web bundle the web-only lane
	// already published (eliminating SPA template mismatches between
	// independent inline builds).
	//
	// Example:
	//   build_args:
	//     - "WEB_ARTIFACT_IMAGE=oxpulse-chat-web:prod-${SHA}"
	BuildArgs []string `yaml:"build_args,omitempty"`

	// PreBuildScript is an absolute path to a bash script that runs BEFORE
	// `docker compose build` for this repo. The script receives two env vars:
	//   DEPLOY_REPO_PATH — SourcePath (the local git checkout)
	//   DEPLOY_SHA       — full commit SHA being built
	// A non-zero exit code aborts the build with the script's stderr.
	//
	// Use case: building a web OCI artifact (Dockerfile.web) that the main
	// Dockerfile consumes via WEB_ARTIFACT_IMAGE build-arg. The pre-build
	// script builds the artifact image so it exists in the local Docker
	// daemon before compose build references it.
	//
	// Example:
	//   pre_build_script: /home/krolik/deploy/krolik-server/scripts/build-web-artifact.sh
	PreBuildScript string `yaml:"pre_build_script,omitempty"`
}

// ImageCacheConfig configures per-repo image caching (build-once-promote).
// When Registry is empty the feature is disabled (the default for every repo
// that does not opt in).
type ImageCacheConfig struct {
	// Registry is the fully-qualified image reference WITHOUT a tag, e.g.
	// "ghcr.io/anatolykoptev/oxpulse-chat". The tree-hash tag is appended
	// as ":tree-<40-char-hex>". When non-empty, image caching is enabled
	// for this repo.
	Registry string `yaml:"registry,omitempty"`

	// Services restricts caching to a subset of the repo's services. When
	// empty, ALL services in the repo entry are cacheable. Use this when a
	// repo builds multiple services from one tree but only some share an
	// identical artifact (e.g. oxpulse-chat-stagingprod is production-mode
	// and cacheable, while oxpulse-chat-staging is test-mode and is not).
	//
	// The pull-before-build path only fires when EVERY service in the repo
	// entry is cacheable — otherwise the non-cacheable services still need
	// a build, so there is no saving from skipping. The push-after-build
	// path fires for each cacheable service individually.
	Services []string `yaml:"services,omitempty"`

	// TokenCommand is a shell command whose STDOUT is a fresh registry
	// token, run immediately before each push and each pull. It lets dozor
	// obtain a short-lived credential (e.g. a GitHub App installation token
	// that expires hourly) instead of relying on an ambient `docker login`
	// that silently expires. The token is piped to `docker login` via stdin
	// and is NEVER logged.
	//
	// Empty (the default) falls back to the DOZOR_IMAGE_CACHE_TOKEN_CMD env
	// var; if that is also empty, dozor uses the ambient ~/.docker/config.json
	// credential (the previous behaviour) and logs that fact at INFO so
	// "relying on ambient credentials" is a stated fact, not an accident.
	//
	// The command is run via `sh -c`, so it may include arguments, pipes,
	// and env expansion. Example: "~/bin/gh-app-token.sh".
	TokenCommand string `yaml:"token_command,omitempty"`

	// TokenUsername is the username passed to `docker login <host> -u
	// <username> --password-stdin`. Registry-specific: GHCR App installation
	// tokens use "x-access-token"; other registries differ.
	//
	// Empty (the default) falls back to the DOZOR_IMAGE_CACHE_TOKEN_USERNAME
	// env var; if that is also empty, defaults to "x-access-token" (the
	// GHCR App-token convention used on this host). Override for registries
	// with a different username convention.
	TokenUsername string `yaml:"token_username,omitempty"`
}

// cacheableServices returns the subset of the repo's services that are
// eligible for image caching. Returns nil when the feature is off
// (ImageCache.Registry == ""), meaning no push or pull attempt is made.
// When ImageCache.Services is empty, all of the repo's Services are
// cacheable; otherwise only the intersection is returned.
func (rc RepoConfig) cacheableServices() []string {
	if rc.ImageCache.Registry == "" {
		return nil
	}
	if len(rc.ImageCache.Services) == 0 {
		return rc.Services
	}
	cached := make(map[string]struct{}, len(rc.ImageCache.Services))
	for _, s := range rc.ImageCache.Services {
		cached[s] = struct{}{}
	}
	var out []string
	for _, svc := range rc.Services {
		if _, ok := cached[svc]; ok {
			out = append(out, svc)
		}
	}
	return out
}

var defaultDebounceWindow = func() time.Duration {
	if v := os.Getenv("DOZOR_DEFAULT_DEBOUNCE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
		slog.Warn("DOZOR_DEFAULT_DEBOUNCE invalid, using 3m", "value", v)
	}
	return 3 * time.Minute
}()

// DebounceWindow returns the configured debounce duration.
// Zero uses the global default (DOZOR_DEFAULT_DEBOUNCE, default 3m).
// Negative (−1) opts out of debouncing (immediate dispatch).
func (rc RepoConfig) DebounceWindow() time.Duration {
	switch {
	case rc.DebounceSeconds < 0:
		return 0
	case rc.DebounceSeconds == 0:
		return defaultDebounceWindow
	default:
		return time.Duration(rc.DebounceSeconds) * time.Second
	}
}

// resolvedKind returns the effective deploy kind (defaulting to KindCompose).
func (rc RepoConfig) resolvedKind() DeployKind {
	return rc.ResolvedKind()
}

// ResolvedKind returns the effective deploy kind (defaulting to KindCompose).
// Exported for use by callers outside the deploy package (e.g. tools.HandleDeploy).
func (rc RepoConfig) ResolvedKind() DeployKind {
	switch rc.Kind {
	case KindBinary:
		return KindBinary
	case KindStatic:
		return KindStatic
	default:
		return KindCompose
	}
}

// profileDefaults defines built-in build_paths/skip_paths presets, versioned
// with the binary so all servers behave identically. Repo configs select a
// preset via `profile:` and may append entries via `build_paths_extra` /
// `skip_paths_extra`. An explicit `build_paths` (or `skip_paths`) overrides
// the corresponding preset list entirely.
//
// CanarySmokeTimeout sets the per-profile default for how long dozor waits for
// the canary's smoke_url to return 200 after a restart. Zero means "use the
// hard fallback (30s)". A per-repo canary_smoke_timeout always wins over this.
var profileDefaults = map[string]struct {
	BuildPaths         []string
	SkipPaths          []string
	CanarySmokeTimeout time.Duration
}{
	"go-flat": {
		BuildPaths:         []string{"*.go", "internal/**", "go.mod", "go.sum", "vendor/**", "Dockerfile", "Makefile"},
		SkipPaths:          []string{"docs/**", "*.md", "*.c4", "bin/**", "deploy/**"},
		CanarySmokeTimeout: 30 * time.Second,
	},
	"go-cmd": {
		BuildPaths:         []string{"cmd/**", "internal/**", "go.mod", "go.sum", "vendor/**", "Dockerfile", "Makefile"},
		SkipPaths:          []string{"docs/**", "*.md", "*.c4", "bin/**"},
		CanarySmokeTimeout: 30 * time.Second,
	},
	"rust": {
		BuildPaths: []string{"src/**", "crates/**", "tests/**", "Cargo.toml", "Cargo.lock", "Dockerfile", "Makefile"},
		SkipPaths:  []string{"docs/**", "*.md", "*.c4", "target/**"},
		// 120s: Rust services with heavy startup (ONNX models, ML warmup) need
		// significantly longer. Incident 2026-05-07: embed-server (4 ONNX models,
		// ~46s warmup) hit the 30s default → silent rollback → prod stale 5h.
		CanarySmokeTimeout: 120 * time.Second,
	},
}

// knownProfileNames returns sorted profile names for error messages.
func knownProfileNames() []string {
	names := make([]string, 0, len(profileDefaults))
	for n := range profileDefaults {
		names = append(names, n)
	}
	// Stable order for deterministic error messages.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	return names
}

// resolveProfile expands Profile + *Extra into BuildPaths / SkipPaths once at
// load time. Explicit BuildPaths/SkipPaths in YAML override the preset for
// that list independently. Returns an error for unknown profiles, extras
// without a profile, or a profile that resolves to an empty BuildPaths.
func resolveProfile(repo string, rc *RepoConfig) error {
	hasExtras := len(rc.BuildPathsExtra) > 0 || len(rc.SkipPathsExtra) > 0
	if rc.Profile == "" {
		if hasExtras {
			return fmt.Errorf("repo %q sets build_paths_extra/skip_paths_extra without a profile", repo)
		}
		return nil
	}

	preset, ok := profileDefaults[rc.Profile]
	if !ok {
		return fmt.Errorf("unknown profile %q in repo %q; known: %v", rc.Profile, repo, knownProfileNames())
	}

	if len(rc.BuildPaths) == 0 {
		merged := make([]string, 0, len(preset.BuildPaths)+len(rc.BuildPathsExtra))
		merged = append(merged, preset.BuildPaths...)
		merged = append(merged, rc.BuildPathsExtra...)
		rc.BuildPaths = merged
	}
	if len(rc.SkipPaths) == 0 {
		merged := make([]string, 0, len(preset.SkipPaths)+len(rc.SkipPathsExtra))
		merged = append(merged, preset.SkipPaths...)
		merged = append(merged, rc.SkipPathsExtra...)
		rc.SkipPaths = merged
	}

	if len(rc.BuildPaths) == 0 {
		return fmt.Errorf("repo %q profile %q resolved to empty build_paths", repo, rc.Profile)
	}
	return nil
}

// Config holds the full deploy webhook configuration.
type Config struct {
	Repos       map[string]RepoConfig `yaml:"repos"`
	Secret      string                `yaml:"-"` // loaded from env: DOZOR_GITHUB_WEBHOOK_SECRET
	GitHubToken string                `yaml:"-"` // loaded from env: DOZOR_GITHUB_TOKEN
}

// validateRepoConfig validates and normalises a single RepoConfig entry.
// It is called once per repo by LoadConfig after profile resolution.
// Mutates rc in-place to fill derived fields (Services from UserServices, etc.).
func validateRepoConfig(repo string, rc *RepoConfig) error {
	if rc.DeployOn != "" && rc.DeployOn != deployOnRelease && rc.DeployOn != deployOnManual {
		return fmt.Errorf("repo %q has invalid deploy_on %q: want \"\", \"release\", or \"manual\"", repo, rc.DeployOn)
	}
	switch rc.resolvedKind() {
	case KindBinary:
		if rc.SourcePath == "" {
			return fmt.Errorf("binary repo %q has no source_path", repo)
		}
		if len(rc.BuildCmd) == 0 {
			return fmt.Errorf("binary repo %q has no build_cmd", repo)
		}
		if len(rc.UserServices) == 0 {
			return fmt.Errorf("binary repo %q has no user_services", repo)
		}
		// Binary repos use UserServices for restart targets; the
		// queue keys/logs/debounce paths all reach for Services. If
		// the operator didn't set Services explicitly, mirror it from
		// UserServices so the queue key is non-empty (empty key would
		// collide with drainPending's "no work pending" sentinel and
		// leave the entry stuck forever) and log lines show the
		// systemd unit names being restarted.
		if len(rc.Services) == 0 {
			rc.Services = append([]string(nil), rc.UserServices...)
		}
	case KindStatic:
		if rc.SourcePath == "" {
			return fmt.Errorf("static repo %q has no source_path", repo)
		}
		if rc.StaticDeployScript == "" {
			return fmt.Errorf("static repo %q has no static_deploy_script", repo)
		}
		// Use repo name as the queue key when Services is not set explicitly.
		if len(rc.Services) == 0 {
			rc.Services = []string{repo}
		}
	default: // KindCompose
		if len(rc.Services) == 0 {
			return fmt.Errorf("compose repo %q has no services", repo)
		}
		if rc.ComposePath == "" {
			return fmt.Errorf("compose repo %q has no compose_path", repo)
		}
	}
	return nil
}

// LoadConfig reads deploy-repos.yaml from the given path.
// Secret is loaded from DOZOR_GITHUB_WEBHOOK_SECRET env var.
//
//nolint:gocognit // pre-existing; kind-specific validation extracted to validateRepoConfig
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read deploy config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse deploy config: %w", err)
	}

	if len(cfg.Repos) == 0 {
		return nil, errors.New("deploy config has no repos")
	}

	cfg.Secret = os.Getenv("DOZOR_GITHUB_WEBHOOK_SECRET")
	cfg.GitHubToken = os.Getenv("DOZOR_GITHUB_TOKEN")

	for repo, rc := range cfg.Repos {
		if err := resolveProfile(repo, &rc); err != nil {
			return nil, err
		}
		if err := validateRepoConfig(repo, &rc); err != nil {
			return nil, err
		}
		cfg.Repos[repo] = rc
	}

	if err := validateMultiTarget(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// validateMultiTarget cross-checks repos that declare more than one deploy
// target (keyed "owner/repo#<suffix>"). Two distinct shapes share a repoKey:
// multi-APP (e.g. piter-now + piter-now#hully, same branch, different apps) and
// multi-BRANCH (e.g. a repo's main + dev, same app, different branches). The
// checks must not conflate them:
//
//   - Same serviceKey (any branch) → FATAL. The queue/debounce coalesces by
//     serviceKey with no branch component (see queue.go), so two targets with
//     the same key collide and one silently never deploys. Empty keys can't
//     occur (validateRepoConfig defaults static→map-key, binary→user_services,
//     compose requires non-empty) — the only way to collide is two identical
//     *explicit* services: lists. Fail loud at config-load.
//   - Same SourcePath ON THE SAME BRANCH → WARN only. Two apps building from one
//     clone on one branch are safe under the serial queue but would race on git
//     once build concurrency is raised above 1. Multi-BRANCH entries sharing a
//     clone (different branches, the documented multi-branch pattern) are NOT
//     flagged — the worker fetches origin/<branch> into the shared clone, which
//     is the intended design.
//
// Single-target repos (the overwhelming common case) skip this entirely. Runs
// after per-entry normalisation, so Services reflects the effective value.
func validateMultiTarget(cfg *Config) error {
	byRepo := make(map[string][]string, len(cfg.Repos))
	for key := range cfg.Repos {
		repoKey := key
		if idx := strings.LastIndex(key, "#"); idx >= 0 {
			repoKey = key[:idx]
		}
		byRepo[repoKey] = append(byRepo[repoKey], key)
	}

	for repoKey, keys := range byRepo {
		if len(keys) < 2 {
			continue
		}
		sort.Strings(keys)
		svcOwner := make(map[string]string, len(keys))
		srcOwner := make(map[string]string, len(keys)) // keyed branch+SourcePath
		for _, key := range keys {
			rc := cfg.Repos[key]

			svc := serviceKey(rc.Services)
			if other, dup := svcOwner[svc]; dup {
				return fmt.Errorf(
					"multi-target repo %q: entries %q and %q resolve to the same service key %q — give each a distinct services:",
					repoKey, other, key, svc)
			}
			svcOwner[svc] = key

			if rc.SourcePath != "" {
				branch := rc.Branch
				if branch == "" {
					branch = "main"
				}
				srcKey := branch + "\x00" + rc.SourcePath
				if other, dup := srcOwner[srcKey]; dup {
					slog.Warn("multi-target repo shares a deploy clone on one branch — safe while build concurrency is 1, but raise DOZOR_BUILD_CONCURRENCY and these will race on the clone; consider distinct source_path clones",
						"repo", repoKey,
						"entries", other+", "+key,
						"branch", branch,
						"source_path", rc.SourcePath)
				}
				srcOwner[srcKey] = key
			}
		}
	}
	return nil
}

// DefaultConfigPath returns ~/.dozor/deploy-repos.yaml or DOZOR_WORKSPACE/deploy-repos.yaml.
func DefaultConfigPath() string {
	ws := os.Getenv("DOZOR_WORKSPACE")
	if ws == "" {
		home, _ := os.UserHomeDir()
		ws = filepath.Join(home, ".dozor")
	}
	return filepath.Join(ws, "deploy-repos.yaml")
}

// Lookup returns the config for a given GitHub repo full name (e.g. "anatolykoptev/ox-browser").
// Returns nil if the repo is not configured.
//
// Deprecated: use LookupBranch to support multi-branch deployments. This
// method returns the first entry whose repo name matches, regardless of branch.
func (c *Config) Lookup(repoFullName string) *RepoConfig {
	return c.LookupBranch(repoFullName, "")
}

// LookupBranch returns the RepoConfig whose (repo, branch) pair matches the
// given push event. branch is the short branch name (e.g. "main", "dev").
//
// Matching rules:
//   - An entry's effective branch is rc.Branch when non-empty, otherwise "main".
//   - If branch == "" the first entry whose repo name matches is returned
//     (backward-compat for callers that don't know the branch yet).
//   - Returns nil if no entry matches.
func (c *Config) LookupBranch(repoFullName, branch string) *RepoConfig {
	for key, rc := range c.Repos {
		// Extract the repo name from the map key (keys may be "owner/repo" or
		// "owner/repo#branch"; the Branch field is the canonical source of truth).
		repoKey := key
		if idx := strings.LastIndex(key, "#"); idx >= 0 {
			repoKey = key[:idx]
		}
		if repoKey != repoFullName {
			continue
		}
		if branch == "" {
			// Caller doesn't filter by branch — return first match.
			cp := rc
			return &cp
		}
		effective := rc.Branch
		if effective == "" {
			effective = "main"
		}
		if effective == branch {
			cp := rc
			return &cp
		}
	}
	return nil
}

// LookupAll returns EVERY RepoConfig whose (repo, branch) pair matches — the
// multi-target form of LookupBranch. A monorepo with several independent deploy
// targets keys them "owner/repo#<suffix>" (e.g. "anatolykoptev/piter-now" and
// "anatolykoptev/piter-now#hully"); both share the same repoKey, so a single
// push fans out to all of them, each later gated by its own BuildPaths filter.
//
// Matching rules mirror LookupBranch (effective branch = rc.Branch or "main";
// branch == "" matches any). Results are sorted by map key so dispatch order is
// deterministic. The common single-target repo returns a one-element slice, so
// callers behave identically to the old single-lookup path.
func (c *Config) LookupAll(repoFullName, branch string) []*RepoConfig {
	keys := make([]string, 0, len(c.Repos))
	for k := range c.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var matches []*RepoConfig
	for _, key := range keys {
		repoKey := key
		if idx := strings.LastIndex(key, "#"); idx >= 0 {
			repoKey = key[:idx]
		}
		if repoKey != repoFullName {
			continue
		}
		rc := c.Repos[key]
		if branch != "" {
			effective := rc.Branch
			if effective == "" {
				effective = "main"
			}
			if effective != branch {
				continue
			}
		}
		cp := rc
		matches = append(matches, &cp)
	}
	return matches
}

// LookupReleaseTargets returns every entry configured to deploy on GitHub
// release events (deploy_on: release OR deploy_on: manual) for repoFullName,
// in deterministic (key-sorted) order via LookupAll. A release event must
// build ONLY these targets — never a random first-match.
//
// The previous LookupBranch(repo, "") fallback returned whichever map entry
// Go's randomised iteration happened to yield first, so for a repo with both
// a release-gated prod target and a push-based canary (keyed
// "owner/repo#dev"), a release could route to the canary whose source clone
// lacks the release commit — surfacing as a "git worktree add: invalid
// reference" build failure, or worse, silently deploying the wrong
// environment while production stays on the old image (issue #169).
//
// Returns nil (NOT a fallback to first-match) when no entry is release-gated;
// the caller logs and ignores the release event. Deploying a target that was
// not configured for releases is exactly the bug this method prevents.
func (c *Config) LookupReleaseTargets(repoFullName string) []*RepoConfig {
	var targets []*RepoConfig
	for _, rc := range c.LookupAll(repoFullName, "") {
		if rc.DeployOn == deployOnRelease || rc.DeployOn == deployOnManual {
			targets = append(targets, rc)
		}
	}
	return targets
}
