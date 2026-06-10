package llmcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolve_EnvBeatsYAML confirms env > YAML > defaults precedence for
// CheckKey (the only YAML-writable field besides GeminiKeys).
func TestResolve_EnvBeatsYAML(t *testing.T) {
	// Unset all LLM env vars first.
	for _, k := range []string{
		"DOZOR_LLM_URL", "DOZOR_LLM_API_KEY", "DOZOR_LLM_MODEL",
		"DOZOR_LLM_CHECK_URL", "DOZOR_LLM_CHECK_API_KEY", "DOZOR_LLM_CHECK_MODELS",
		"DOZOR_LLM_FALLBACK_URL", "DOZOR_LLM_FALLBACK_API_KEY", "DOZOR_LLM_FALLBACK_MODEL",
		"DOZOR_GEMINI_API_KEYS", "GEMINI_API_KEY",
	} {
		t.Setenv(k, "")
	}

	// Env supplies the check key.
	t.Setenv("DOZOR_LLM_CHECK_API_KEY", "env-check-key")

	// YAML also supplies a proxy api-key (would fill CheckKey if env is absent).
	yamlBody := `
api-keys:
  - yaml-proxy-key
gemini-api-key:
  - api-key: yaml-gemini-key1
  - api-key: yaml-gemini-key2
`
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Resolve(yamlPath)
	if err != nil {
		t.Fatal(err)
	}

	// Env wins for CheckKey.
	if c.CheckKey != "env-check-key" {
		t.Errorf("CheckKey: got %q, want env-check-key (env wins)", c.CheckKey)
	}
	// YAML fills GeminiKeys (no env for DOZOR_GEMINI_API_KEYS).
	if len(c.GeminiKeys) != 2 {
		t.Errorf("GeminiKeys: got %d, want 2 (YAML fills)", len(c.GeminiKeys))
	}
	if len(c.GeminiKeys) > 0 && c.GeminiKeys[0] != "yaml-gemini-key1" {
		t.Errorf("GeminiKeys[0]: got %q, want yaml-gemini-key1", c.GeminiKeys[0])
	}
}

// TestResolve_YAMLFillsCheckKeyWhenEnvEmpty confirms YAML fills CheckKey
// when DOZOR_LLM_CHECK_API_KEY is not set.
func TestResolve_YAMLFillsCheckKeyWhenEnvEmpty(t *testing.T) {
	for _, k := range []string{
		"DOZOR_LLM_URL", "DOZOR_LLM_API_KEY", "DOZOR_LLM_MODEL",
		"DOZOR_LLM_CHECK_URL", "DOZOR_LLM_CHECK_API_KEY", "DOZOR_LLM_CHECK_MODELS",
		"DOZOR_LLM_FALLBACK_URL", "DOZOR_LLM_FALLBACK_API_KEY", "DOZOR_LLM_FALLBACK_MODEL",
		"DOZOR_GEMINI_API_KEYS", "GEMINI_API_KEY",
	} {
		t.Setenv(k, "")
	}

	yamlBody := `
api-keys:
  - yaml-proxy-key
`
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Resolve(yamlPath)
	if err != nil {
		t.Fatal(err)
	}

	// YAML fills CheckKey when env is empty.
	if c.CheckKey != "yaml-proxy-key" {
		t.Errorf("CheckKey: got %q, want yaml-proxy-key (YAML fills empty env)", c.CheckKey)
	}
}

// TestResolve_DefaultURL confirms default PrimaryURL when no env or YAML.
func TestResolve_DefaultURL(t *testing.T) {
	for _, k := range []string{
		"DOZOR_LLM_URL", "DOZOR_LLM_API_KEY", "DOZOR_LLM_MODEL",
		"DOZOR_LLM_CHECK_URL", "DOZOR_LLM_CHECK_API_KEY", "DOZOR_LLM_CHECK_MODELS",
		"DOZOR_LLM_FALLBACK_URL", "DOZOR_LLM_FALLBACK_API_KEY", "DOZOR_LLM_FALLBACK_MODEL",
		"DOZOR_GEMINI_API_KEYS", "GEMINI_API_KEY",
	} {
		t.Setenv(k, "")
	}

	c, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if c.PrimaryURL != "http://127.0.0.1:8787/v1" {
		t.Errorf("PrimaryURL default: got %q", c.PrimaryURL)
	}
}

// TestResolve_FallbackURLNotInherited confirms llmcfg does NOT inherit
// PrimaryURL into FallbackURL — that's the provider layer's concern.
func TestResolve_FallbackURLNotInherited(t *testing.T) {
	for _, k := range []string{
		"DOZOR_LLM_URL", "DOZOR_LLM_API_KEY", "DOZOR_LLM_MODEL",
		"DOZOR_LLM_CHECK_URL", "DOZOR_LLM_CHECK_API_KEY", "DOZOR_LLM_CHECK_MODELS",
		"DOZOR_LLM_FALLBACK_URL", "DOZOR_LLM_FALLBACK_API_KEY", "DOZOR_LLM_FALLBACK_MODEL",
		"DOZOR_GEMINI_API_KEYS", "GEMINI_API_KEY",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("DOZOR_LLM_URL", "http://primary/v1")
	t.Setenv("DOZOR_LLM_API_KEY", "primary-key")

	c, _ := Resolve("")
	// FallbackURL should be empty — provider layer resolves inheritance.
	if c.FallbackURL != "" {
		t.Errorf("FallbackURL: got %q, want empty (no env set)", c.FallbackURL)
	}
}

// TestResolve_GeminiKeysCSVEnv confirms DOZOR_GEMINI_API_KEYS CSV env var.
func TestResolve_GeminiKeysCSVEnv(t *testing.T) {
	for _, k := range []string{
		"DOZOR_LLM_URL", "DOZOR_LLM_API_KEY", "DOZOR_LLM_MODEL",
		"DOZOR_LLM_CHECK_URL", "DOZOR_LLM_CHECK_API_KEY", "DOZOR_LLM_CHECK_MODELS",
		"DOZOR_LLM_FALLBACK_URL", "DOZOR_LLM_FALLBACK_API_KEY", "DOZOR_LLM_FALLBACK_MODEL",
		"DOZOR_GEMINI_API_KEYS", "GEMINI_API_KEY",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("DOZOR_GEMINI_API_KEYS", "key1,key2,key3")

	c, _ := Resolve("")
	if len(c.GeminiKeys) != 3 {
		t.Errorf("GeminiKeys: got %d, want 3", len(c.GeminiKeys))
	}
	if c.GeminiKeys[1] != "key2" {
		t.Errorf("GeminiKeys[1]: got %q, want key2", c.GeminiKeys[1])
	}
}

// TestResolve_GeminiKeyFallbackSingleEnv confirms GEMINI_API_KEY single-key
// backwards compat: if DOZOR_GEMINI_API_KEYS is not set, GEMINI_API_KEY
// populates GeminiKeys[0].
func TestResolve_GeminiKeyFallbackSingleEnv(t *testing.T) {
	for _, k := range []string{
		"DOZOR_LLM_URL", "DOZOR_LLM_API_KEY", "DOZOR_LLM_MODEL",
		"DOZOR_LLM_CHECK_URL", "DOZOR_LLM_CHECK_API_KEY", "DOZOR_LLM_CHECK_MODELS",
		"DOZOR_LLM_FALLBACK_URL", "DOZOR_LLM_FALLBACK_API_KEY", "DOZOR_LLM_FALLBACK_MODEL",
		"DOZOR_GEMINI_API_KEYS", "GEMINI_API_KEY",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("GEMINI_API_KEY", "single-gemini-key")

	c, _ := Resolve("")
	if len(c.GeminiKeys) != 1 {
		t.Errorf("GeminiKeys: got %d, want 1", len(c.GeminiKeys))
	}
	if c.GeminiKeys[0] != "single-gemini-key" {
		t.Errorf("GeminiKeys[0]: got %q, want single-gemini-key", c.GeminiKeys[0])
	}
}

// TestResolve_NoYAML confirms no error when yamlPath is empty.
func TestResolve_NoYAML(t *testing.T) {
	for _, k := range []string{
		"DOZOR_LLM_URL", "DOZOR_LLM_API_KEY", "DOZOR_LLM_MODEL",
		"DOZOR_LLM_CHECK_URL", "DOZOR_LLM_CHECK_API_KEY", "DOZOR_LLM_CHECK_MODELS",
		"DOZOR_LLM_FALLBACK_URL", "DOZOR_LLM_FALLBACK_API_KEY", "DOZOR_LLM_FALLBACK_MODEL",
		"DOZOR_GEMINI_API_KEYS", "GEMINI_API_KEY",
	} {
		t.Setenv(k, "")
	}

	_, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve with empty yamlPath should not error: %v", err)
	}
}

// TestResolve_CheckModelsDefaultsToChain: with DOZOR_LLM_CHECK_MODELS unset,
// the canary list mirrors the production fallback chain (drift-proof default);
// an explicit value still overrides.
func TestResolve_CheckModelsDefaultsToChain(t *testing.T) {
	t.Setenv("DOZOR_LLM_CHECK_MODELS", "")
	t.Setenv("DOZOR_LLM_MODEL_FALLBACK", "m1,m2,m3")
	t.Setenv("LLM_MODEL_FALLBACK", "ignored")

	cfg, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := strings.Join(cfg.CheckModels, ","); got != "m1,m2,m3" {
		t.Errorf("CheckModels = %q, want chain mirror m1,m2,m3", got)
	}

	t.Setenv("DOZOR_LLM_CHECK_MODELS", "explicit-model")
	cfg, err = Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := strings.Join(cfg.CheckModels, ","); got != "explicit-model" {
		t.Errorf("explicit DOZOR_LLM_CHECK_MODELS must override chain, got %q", got)
	}
}
