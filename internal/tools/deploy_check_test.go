package tools

import (
	"testing"

	"github.com/anatolykoptev/dozor/internal/deploy"
)

func TestFindRepoByService(t *testing.T) {
	cfg := &deploy.Config{
		Repos: map[string]deploy.RepoConfig{
			"anatolykoptev/oxpulse-chat": {
				SourcePath: "/home/krolik/src/oxpulse-chat",
				Services:   []string{"oxpulse-chat"},
			},
			"anatolykoptev/vaelor": {
				SourcePath:   "/home/krolik/src/vaelor",
				UserServices: []string{"vaelor-orchestrator", "vaelor-content"},
			},
		},
	}

	t.Run("match in Services", func(t *testing.T) {
		repo, rc, ok := findRepoByService(cfg, "oxpulse-chat")
		if !ok {
			t.Fatal("expected hit, got miss")
		}
		if repo != "anatolykoptev/oxpulse-chat" {
			t.Errorf("repo: got %q, want anatolykoptev/oxpulse-chat", repo)
		}
		if rc.SourcePath != "/home/krolik/src/oxpulse-chat" {
			t.Errorf("source path mismatch: %q", rc.SourcePath)
		}
	})

	t.Run("match in UserServices (binary kind)", func(t *testing.T) {
		repo, _, ok := findRepoByService(cfg, "vaelor-content")
		if !ok {
			t.Fatal("expected hit on UserServices, got miss")
		}
		if repo != "anatolykoptev/vaelor" {
			t.Errorf("repo: got %q, want anatolykoptev/vaelor", repo)
		}
	})

	t.Run("no match", func(t *testing.T) {
		_, _, ok := findRepoByService(cfg, "ghost-service")
		if ok {
			t.Fatal("expected miss, got hit")
		}
	})
}

func TestEffectiveKind(t *testing.T) {
	cases := []struct {
		in   deploy.DeployKind
		want string
	}{
		{"", "compose"},
		{deploy.KindCompose, "compose"},
		{deploy.KindBinary, "binary"},
		{deploy.KindStatic, "static"},
	}
	for _, c := range cases {
		got := effectiveKind(deploy.RepoConfig{Kind: c.in})
		if got != c.want {
			t.Errorf("Kind=%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortSHA(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"abc", "abc"},
		{"abcdefgh", "abcdefgh"},
		{"abcdefghi", "abcdefgh"},
		{"4908b01f4e73f5506f145bd17efd6873f22b7f0b", "4908b01f"},
	}
	for _, c := range cases {
		got := short(c.in)
		if got != c.want {
			t.Errorf("short(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
