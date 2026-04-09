// Package deploy implements GitHub webhook-driven Docker service rebuilds.
package deploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// RepoConfig maps a GitHub repository to Docker Compose services.
type RepoConfig struct {
	ComposePath string   `yaml:"compose_path"`
	Services    []string `yaml:"services"`
	SourcePath  string   `yaml:"source_path"`
	NoCache     bool     `yaml:"no_cache"`
}

// Config holds the full deploy webhook configuration.
type Config struct {
	Repos  map[string]RepoConfig `yaml:"repos"`
	Secret string                `yaml:"-"` // loaded from env, never from file
}

// LoadConfig reads deploy-repos.yaml from the given path.
// Secret is loaded from DOZOR_GITHUB_WEBHOOK_SECRET env var.
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

	// Validate: each repo must have at least one service
	for repo, rc := range cfg.Repos {
		if len(rc.Services) == 0 {
			return nil, fmt.Errorf("repo %q has no services", repo)
		}
		if rc.ComposePath == "" {
			return nil, fmt.Errorf("repo %q has no compose_path", repo)
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
