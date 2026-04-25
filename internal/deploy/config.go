// Package deploy implements GitHub webhook-driven service rebuilds.
// Supports two deploy kinds:
//   - "compose" (default): docker compose build + up
//   - "binary": git pull + go build + systemctl --user restart
package deploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
)

// RepoConfig maps a GitHub repository to its deploy strategy.
type RepoConfig struct {
	// Kind selects the deploy strategy. Default: "compose".
	Kind DeployKind `yaml:"kind,omitempty"`

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

	// SkipPaths is a documentation-only list of paths the operator wants to
	// be sure never trigger a rebuild. Today it is purely informational —
	// BuildPaths is the source of truth for the filter decision.
	SkipPaths []string `yaml:"skip_paths,omitempty"`

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
}

// DebounceWindow returns the configured debounce duration, or 0 if disabled.
func (rc RepoConfig) DebounceWindow() time.Duration {
	if rc.DebounceSeconds <= 0 {
		return 0
	}
	return time.Duration(rc.DebounceSeconds) * time.Second
}

// resolvedKind returns the effective deploy kind (defaulting to KindCompose).
func (rc RepoConfig) resolvedKind() DeployKind {
	if rc.Kind == KindBinary {
		return KindBinary
	}
	return KindCompose
}

// profileDefaults defines built-in build_paths/skip_paths presets, versioned
// with the binary so all servers behave identically. Repo configs select a
// preset via `profile:` and may append entries via `build_paths_extra` /
// `skip_paths_extra`. An explicit `build_paths` (or `skip_paths`) overrides
// the corresponding preset list entirely.
var profileDefaults = map[string]struct {
	BuildPaths []string
	SkipPaths  []string
}{
	"go-flat": {
		BuildPaths: []string{"*.go", "internal/**", "go.mod", "go.sum", "vendor/**", "Dockerfile", "Makefile"},
		SkipPaths:  []string{"docs/**", "*.md", "bin/**", "deploy/**"},
	},
	"go-cmd": {
		BuildPaths: []string{"cmd/**", "internal/**", "go.mod", "go.sum", "vendor/**", "Dockerfile", "Makefile"},
		SkipPaths:  []string{"docs/**", "*.md", "bin/**"},
	},
	"rust": {
		BuildPaths: []string{"src/**", "crates/**", "tests/**", "Cargo.toml", "Cargo.lock", "Dockerfile", "Makefile"},
		SkipPaths:  []string{"docs/**", "*.md", "target/**"},
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
	Repos  map[string]RepoConfig `yaml:"repos"`
	Secret string                `yaml:"-"` // loaded from env, never from file
}

// LoadConfig reads deploy-repos.yaml from the given path.
// Secret is loaded from DOZOR_GITHUB_WEBHOOK_SECRET env var.
//
//nolint:gocognit // pre-existing validation switch; complexity was borderline before Duration fields were added
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

	for repo, rc := range cfg.Repos {
		if err := resolveProfile(repo, &rc); err != nil {
			return nil, err
		}
		cfg.Repos[repo] = rc

		switch rc.resolvedKind() {
		case KindBinary:
			if rc.SourcePath == "" {
				return nil, fmt.Errorf("binary repo %q has no source_path", repo)
			}
			if len(rc.BuildCmd) == 0 {
				return nil, fmt.Errorf("binary repo %q has no build_cmd", repo)
			}
			if len(rc.UserServices) == 0 {
				return nil, fmt.Errorf("binary repo %q has no user_services", repo)
			}
		default: // KindCompose
			if len(rc.Services) == 0 {
				return nil, fmt.Errorf("compose repo %q has no services", repo)
			}
			if rc.ComposePath == "" {
				return nil, fmt.Errorf("compose repo %q has no compose_path", repo)
			}
		}
	}

	return &cfg, nil
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
func (c *Config) Lookup(repoFullName string) *RepoConfig {
	rc, ok := c.Repos[repoFullName]
	if !ok {
		return nil
	}
	return &rc
}
