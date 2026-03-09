package engine

import (
	"fmt"
	"strings"
)

// knownBinaries maps binary name -> "owner/repo".
var knownBinaries = map[string]string{
	"gh": "cli/cli", "lazygit": "jesseduffield/lazygit",
	"lazydocker": "jesseduffield/lazydocker", "fzf": "junegunn/fzf",
	"bat": "sharkdp/bat", "fd": "sharkdp/fd",
	"ripgrep": "BurntSushi/ripgrep", "rg": "BurntSushi/ripgrep",
	"delta": "dandavison/delta", "eza": "eza-community/eza",
	"zoxide": "ajeetdsouza/zoxide", "starship": "starship/starship",
	"yq": "mikefarah/yq", "jq": "jqlang/jq",
	"dust": "bootandy/dust", "duf": "muesli/duf",
	"btop": "aristocratos/btop", "bottom": "ClementTsang/bottom",
	"btm": "ClementTsang/bottom", "procs": "dalance/procs",
	"hyperfine": "sharkdp/hyperfine", "tokei": "XAMPPRocky/tokei",
	"sd": "chmln/sd", "tealdeer": "dbrgn/tealdeer",
	"tldr": "dbrgn/tealdeer", "glow": "charmbracelet/glow",
	"navi": "denisidoro/navi", "atuin": "atuinsh/atuin",
	"zellij": "zellij-org/zellij", "k9s": "derailed/k9s",
	"kubectl": "kubernetes/kubernetes", "stern": "stern/stern",
	"dive": "wagoodman/dive", "act": "nektos/act",
	"task": "go-task/task", "just": "casey/just",
	"watchexec": "watchexec/watchexec", "lsd": "lsd-rs/lsd",
	"broot": "Canop/broot", "croc": "schollz/croc",
	"caddy": "caddyserver/caddy", "mkcert": "FiloSottile/mkcert",
	"age": "FiloSottile/age", "sops": "getsops/sops",
	"direnv": "direnv/direnv", "mise": "jdx/mise",
	"uv": "astral-sh/uv", "ruff": "astral-sh/ruff",
	"deno": "denoland/deno", "bun": "oven-sh/bun",
	"helix": "helix-editor/helix", "hx": "helix-editor/helix",
	"neovim": "neovim/neovim", "nvim": "neovim/neovim",
	"micro": "zyedidia/micro", "yazi": "sxyazi/yazi",
	"doggo": "mr-karan/doggo", "gum": "charmbracelet/gum",
	"charm": "charmbracelet/charm", "cloudflared": "cloudflare/cloudflared",
	"minio": "minio/minio", "mc": "minio/mc",
	"restic": "restic/restic", "pget": "Code-Hex/pget",
	"curlie": "rs/curlie", "xh": "ducaale/xh",
	"httpie": "httpie/cli", "hey": "rakyll/hey",
	"vegeta": "tsenart/vegeta",
}

// versionFlags maps binary name -> flag to get version.
var versionFlags = map[string]string{
	"gh": "--version", "lazygit": "--version",
	"nvim": "--version", "neovim": "--version",
	"kubectl": "version --client --short 2>/dev/null || kubectl version --client",
	"jq": "--version", "yq": "--version",
	"micro": "--version", "mc": "--version",
	"just": "--version", "task": "--version",
	"deno": "--version", "bun": "--version",
	"uv": "--version", "ruff": "--version",
	"starship": "--version",
}

// extractVersion extracts a semver-like version from version command output.
func extractVersion(output string) string {
	output = strings.TrimSpace(output)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, word := range strings.Fields(line) {
			v := normalizeVersion(word)
			if v != "" && isVersionLike(v) {
				return v
			}
		}
	}
	return ""
}

// normalizeVersion strips common prefixes (v, V) from version strings.
func normalizeVersion(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	s = strings.TrimRight(s, ",;()[]")
	return s
}

// isVersionLike checks if a string looks like a version number (N.N or N.N.N).
func isVersionLike(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	for _, p := range parts {
		dashIdx := strings.Index(p, "-")
		numPart := p
		if dashIdx > 0 {
			numPart = p[:dashIdx]
		}
		if numPart == "" {
			return false
		}
		for _, c := range numPart {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// isSkippableAsset returns true for checksums, signatures, and non-binary file types.
func isSkippableAsset(name string) bool {
	skipSuffixes := []string{
		".sha256", ".sha512", ".sig", ".asc", ".sbom", ".txt", ".json",
	}
	for _, s := range skipSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// matchesPlatform returns true when name contains at least one OS pattern and one arch pattern.
func matchesPlatform(name string, osPatterns, archPatterns []string) bool {
	osMatch := false
	for _, p := range osPatterns {
		if strings.Contains(name, p) {
			osMatch = true
			break
		}
	}
	if !osMatch {
		return false
	}
	for _, p := range archPatterns {
		if strings.Contains(name, p) {
			return true
		}
	}
	return false
}

// scoreAssetFormat returns a preference score for a matching asset and whether it should be skipped.
func scoreAssetFormat(name, binaryName string) (score int, skip bool) {
	switch {
	case strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz"):
		score = 10
	case strings.HasSuffix(name, ".tar.xz"):
		score = 9
	case strings.HasSuffix(name, ".gz") && !strings.HasSuffix(name, ".tar.gz"):
		score = 8
	case strings.HasSuffix(name, ".zip"):
		score = 7
	default:
		pkgSuffixes := []string{".deb", ".rpm", ".apk", ".msi", ".dmg", ".pkg"}
		for _, s := range pkgSuffixes {
			if strings.HasSuffix(name, s) {
				return 0, true
			}
		}
		score = 6
	}
	if strings.Contains(name, strings.ToLower(binaryName)) {
		score += 2
	}
	if strings.Contains(name, "musl") {
		score--
	}
	return score, false
}

// FormatUpdatesCheck creates a human-readable update check report.
func FormatUpdatesCheck(binaries []TrackedBinary) string {
	if len(binaries) == 0 {
		return "No tracked binaries found.\nConfigure DOZOR_TRACKED_BINARIES or install known CLIs to ~/.local/bin/."
	}
	var b strings.Builder
	b.WriteString("Binary Updates Check\n")
	b.WriteString(strings.Repeat("=", 40))
	b.WriteString("\n\n")
	updates, ok, skipped, errs := 0, 0, 0, 0
	for _, bin := range binaries {
		switch bin.Status {
		case "UPDATE":
			updates++
			fmt.Fprintf(&b, "[UPDATE] %s: %s -> %s\n", bin.Name, bin.CurrentVersion, bin.LatestVersion)
			fmt.Fprintf(&b, "  %s\n", bin.ReleaseURL)
		case "OK":
			ok++
			fmt.Fprintf(&b, "[OK] %s: %s (latest)\n", bin.Name, bin.CurrentVersion)
		case "SKIP":
			skipped++
			fmt.Fprintf(&b, "[SKIP] %s: %s\n", bin.Name, bin.Error)
		case "ERROR":
			errs++
			fmt.Fprintf(&b, "[ERROR] %s: %s\n", bin.Name, bin.Error)
		}
	}
	b.WriteString("\n")
	parts := []string{fmt.Sprintf("%d update(s) available", updates)}
	if ok > 0 {
		parts = append(parts, fmt.Sprintf("%d up to date", ok))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	if errs > 0 {
		parts = append(parts, fmt.Sprintf("%d error(s)", errs))
	}
	fmt.Fprintf(&b, "Summary: %s\n", strings.Join(parts, ", "))
	return b.String()
}
