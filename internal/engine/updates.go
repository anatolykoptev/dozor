package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
)

const (
	// archAMD64 is the Go architecture name for x86-64.
	archAMD64 = "amd64"
	// archARM64 is the Go architecture name for ARM 64-bit.
	archARM64 = "arm64"
)

// UpdatesCollector checks for binary updates via GitHub releases.
type UpdatesCollector struct {
	transport *Transport
	cfg       Config
}

// knownBinaries maps binary name -> "owner/repo".
// Used for auto-detection in ~/.local/bin/.
var knownBinaries = map[string]string{
	"gh":           "cli/cli",
	"lazygit":      "jesseduffield/lazygit",
	"lazydocker":   "jesseduffield/lazydocker",
	"fzf":          "junegunn/fzf",
	"bat":          "sharkdp/bat",
	"fd":           "sharkdp/fd",
	"ripgrep":      "BurntSushi/ripgrep",
	"rg":           "BurntSushi/ripgrep",
	"delta":        "dandavison/delta",
	"eza":          "eza-community/eza",
	"zoxide":       "ajeetdsouza/zoxide",
	"starship":     "starship/starship",
	"yq":           "mikefarah/yq",
	"jq":           "jqlang/jq",
	"dust":         "bootandy/dust",
	"duf":          "muesli/duf",
	"btop":         "aristocratos/btop",
	"bottom":       "ClementTsang/bottom",
	"btm":          "ClementTsang/bottom",
	"procs":        "dalance/procs",
	"hyperfine":    "sharkdp/hyperfine",
	"tokei":        "XAMPPRocky/tokei",
	"sd":           "chmln/sd",
	"tealdeer":     "dbrgn/tealdeer",
	"tldr":         "dbrgn/tealdeer",
	"glow":         "charmbracelet/glow",
	"navi":         "denisidoro/navi",
	"atuin":        "atuinsh/atuin",
	"zellij":       "zellij-org/zellij",
	"k9s":          "derailed/k9s",
	"kubectl":      "kubernetes/kubernetes",
	"stern":        "stern/stern",
	"dive":         "wagoodman/dive",
	"act":          "nektos/act",
	"task":         "go-task/task",
	"just":         "casey/just",
	"watchexec":    "watchexec/watchexec",
	"lsd":          "lsd-rs/lsd",
	"broot":        "Canop/broot",
	"croc":         "schollz/croc",
	"caddy":        "caddyserver/caddy",
	"mkcert":       "FiloSottile/mkcert",
	"age":          "FiloSottile/age",
	"sops":         "getsops/sops",
	"direnv":       "direnv/direnv",
	"mise":         "jdx/mise",
	"uv":           "astral-sh/uv",
	"ruff":         "astral-sh/ruff",
	"deno":         "denoland/deno",
	"bun":          "oven-sh/bun",
	"helix":        "helix-editor/helix",
	"hx":           "helix-editor/helix",
	"neovim":       "neovim/neovim",
	"nvim":         "neovim/neovim",
	"micro":        "zyedidia/micro",
	"yazi":         "sxyazi/yazi",
	"doggo":        "mr-karan/doggo",
	"gum":          "charmbracelet/gum",
	"charm":        "charmbracelet/charm",
	"cloudflared":  "cloudflare/cloudflared",
	"minio":        "minio/minio",
	"mc":           "minio/mc",
	"restic":       "restic/restic",
	"pget":         "Code-Hex/pget",
	"curlie":       "rs/curlie",
	"xh":           "ducaale/xh",
	"httpie":       "httpie/cli",
	"hey":          "rakyll/hey",
	"vegeta":       "tsenart/vegeta",
}

// versionFlags maps binary name -> flag to get version.
// Defaults to "--version" if not listed here.
var versionFlags = map[string]string{
	"gh":       "--version",
	"lazygit":  "--version",
	"nvim":     "--version",
	"neovim":   "--version",
	"kubectl":  "version --client --short 2>/dev/null || kubectl version --client",
	"jq":       "--version",
	"yq":       "--version",
	"micro":    "--version",
	"mc":       "--version",
	"just":     "--version",
	"task":     "--version",
	"deno":     "--version",
	"bun":      "--version",
	"uv":       "--version",
	"ruff":     "--version",
	"starship": "--version",
}

// githubRelease is the subset of GitHub API response we need.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	HTMLURL string        `json:"html_url"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// CheckUpdates scans configured + auto-detected binaries and returns their status.
func (u *UpdatesCollector) CheckUpdates(ctx context.Context) []TrackedBinary {
	binaries := u.discoverBinaries(ctx)
	if len(binaries) == 0 {
		return nil
	}

	for i := range binaries {
		u.checkBinary(ctx, &binaries[i])
	}

	return binaries
}

// InstallUpdate downloads and installs the latest release for a binary.
func (u *UpdatesCollector) InstallUpdate(ctx context.Context, name string) (string, error) {
	binaries := u.discoverBinaries(ctx)
	var target *TrackedBinary
	for i := range binaries {
		if binaries[i].Name == name {
			target = &binaries[i]
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("binary %q not found in tracked binaries", name)
	}

	// Get current version
	target.CurrentVersion = u.getCurrentVersion(ctx, target.Name, target.Path)

	// Fetch release (single API call, reused for install)
	release, err := u.getLatestRelease(ctx, target.Owner, target.Repo)
	if err != nil {
		return "", fmt.Errorf("cannot check %s: %w", name, err)
	}

	target.LatestVersion = normalizeVersion(release.TagName)
	target.ReleaseURL = release.HTMLURL

	if target.CurrentVersion != "" && target.CurrentVersion == target.LatestVersion {
		return fmt.Sprintf("%s is already at latest version (%s)", name, target.CurrentVersion), nil
	}

	return u.downloadAndInstall(ctx, target, release)
}

// discoverBinaries builds the list of binaries to check from config + auto-detect.
func (u *UpdatesCollector) discoverBinaries(ctx context.Context) []TrackedBinary {
	seenName := make(map[string]bool)
	seenRepo := make(map[string]bool)
	var binaries []TrackedBinary

	// 1. Config-based binaries (higher priority)
	for _, cfg := range u.cfg.TrackedBinaries {
		if seenName[cfg.Binary] {
			continue
		}
		repoKey := cfg.Owner + "/" + cfg.Repo
		if seenRepo[repoKey] {
			continue
		}
		seenName[cfg.Binary] = true
		seenRepo[repoKey] = true
		path := u.findBinaryPath(ctx, cfg.Binary)
		binaries = append(binaries, TrackedBinary{
			Name:  cfg.Binary,
			Path:  path,
			Owner: cfg.Owner,
			Repo:  cfg.Repo,
		})
	}

	// 2. Auto-detect from ~/.local/bin/
	binaries = append(binaries, u.autoDetectFromLocalBin(ctx, seenName, seenRepo)...)

	return binaries
}

// autoDetectFromLocalBin scans ~/.local/bin/ for known binaries not already tracked.
func (u *UpdatesCollector) autoDetectFromLocalBin(ctx context.Context, seenName, seenRepo map[string]bool) []TrackedBinary {
	res := u.transport.ExecuteUnsafe(ctx, "ls -1 $HOME/.local/bin/ 2>/dev/null")
	if !res.Success {
		return nil
	}

	var binaries []TrackedBinary
	for _, name := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		name = strings.TrimSpace(name)
		if name == "" || seenName[name] {
			continue
		}
		ownerRepo, ok := knownBinaries[name]
		if !ok {
			continue
		}
		if seenRepo[ownerRepo] {
			continue
		}
		parts := strings.SplitN(ownerRepo, "/", 2)
		if len(parts) != 2 {
			continue
		}
		seenName[name] = true
		seenRepo[ownerRepo] = true
		binaries = append(binaries, TrackedBinary{
			Name:  name,
			Path:  "$HOME/.local/bin/" + name,
			Owner: parts[0],
			Repo:  parts[1],
		})
	}
	return binaries
}

// findBinaryPath locates a binary using `which`.
func (u *UpdatesCollector) findBinaryPath(ctx context.Context, name string) string {
	res := u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("which %s 2>/dev/null", name))
	if res.Success {
		return strings.TrimSpace(res.Stdout)
	}
	return ""
}

// checkBinary fetches the current version and latest GitHub release.
func (u *UpdatesCollector) checkBinary(ctx context.Context, b *TrackedBinary) {
	// Get current version
	b.CurrentVersion = u.getCurrentVersion(ctx, b.Name, b.Path)
	if b.CurrentVersion == "" {
		b.Status = "SKIP"
		b.Error = "version unknown"
		return
	}

	// Get latest version from GitHub
	release, err := u.getLatestRelease(ctx, b.Owner, b.Repo)
	if err != nil {
		b.Status = "ERROR"
		b.Error = err.Error()
		return
	}

	b.LatestVersion = normalizeVersion(release.TagName)
	b.ReleaseURL = release.HTMLURL

	if b.CurrentVersion == b.LatestVersion {
		b.Status = "OK"
	} else {
		b.Status = "UPDATE"
	}
}

// getCurrentVersion runs the binary's version command and extracts the version string.
func (u *UpdatesCollector) getCurrentVersion(ctx context.Context, name, path string) string {
	binary := name
	if path != "" {
		binary = path
	}

	flag := "--version"
	if f, ok := versionFlags[name]; ok {
		flag = f
	}

	cmd := fmt.Sprintf("%s %s 2>/dev/null | head -3", binary, flag)
	res := u.transport.ExecuteUnsafe(ctx, cmd)
	if !res.Success || res.Stdout == "" {
		return ""
	}

	return extractVersion(res.Stdout)
}

// getLatestRelease fetches the latest release from GitHub API.
func (u *UpdatesCollector) getLatestRelease(ctx context.Context, owner, repo string) (*githubRelease, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)

	var cmd string
	if u.cfg.GitHubToken != "" {
		cmd = fmt.Sprintf(
			"GITHUB_TOKEN=%s curl -sfL -H 'Accept: application/vnd.github+json' -H 'Authorization: Bearer '\"$GITHUB_TOKEN\" '%s'",
			SanitizeForShell(u.cfg.GitHubToken), apiURL,
		)
	} else {
		cmd = fmt.Sprintf("curl -sfL -H 'Accept: application/vnd.github+json' '%s'", apiURL)
	}

	res := u.transport.ExecuteUnsafe(ctx, cmd)
	if !res.Success {
		return nil, fmt.Errorf("GitHub API request failed: %s", strings.TrimSpace(res.Stderr))
	}
	if res.Stdout == "" {
		return nil, errors.New("empty response from GitHub API")
	}

	var release githubRelease
	if err := json.Unmarshal([]byte(res.Stdout), &release); err != nil {
		return nil, fmt.Errorf("failed to parse GitHub response: %w", err)
	}

	if release.TagName == "" {
		return nil, fmt.Errorf("no releases found for %s/%s", owner, repo)
	}

	return &release, nil
}

// downloadAndInstall downloads the matching asset and replaces the binary.
func (u *UpdatesCollector) downloadAndInstall(ctx context.Context, b *TrackedBinary, release *githubRelease) (string, error) {
	asset := u.pickAsset(release.Assets, b.Name)
	if asset == nil {
		return "", fmt.Errorf("no matching asset found for %s (platform: %s/%s)", b.Name, u.detectOS(), u.detectArch())
	}

	installPath := b.Path
	if installPath == "" {
		installPath = "$HOME/.local/bin/" + b.Name
	}

	// Create temp dir with mktemp (secure, unpredictable)
	res := u.transport.ExecuteUnsafe(ctx, "mktemp -d ${TMPDIR:-/tmp}/dozor-update-XXXXXXXXXX")
	if !res.Success {
		return "", fmt.Errorf("failed to create temp dir: %s", res.Stderr)
	}
	tmpDir := strings.TrimSpace(res.Stdout)
	if tmpDir == "" {
		return "", errors.New("mktemp returned empty path")
	}

	// Download using token from env var (avoids leaking in process table)
	downloadPath := tmpDir + "/" + asset.Name
	cmd := u.buildCurlCmd(downloadPath, asset.BrowserDownloadURL)
	res = u.transport.ExecuteUnsafe(ctx, cmd)
	if !res.Success {
		u.transport.ExecuteUnsafe(ctx, "rm -rf '"+tmpDir+"'")
		return "", fmt.Errorf("download failed: %s", res.Stderr)
	}

	// Extract or copy binary
	binaryPath, err := u.extractBinary(ctx, downloadPath, tmpDir, b.Name)
	if err != nil {
		u.transport.ExecuteUnsafe(ctx, "rm -rf '"+tmpDir+"'")
		return "", fmt.Errorf("extraction failed: %w", err)
	}

	// Handle "text file busy" — move old binary first
	// Use double quotes for installPath since it may contain $HOME
	u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("mv \"%s\" \"%s.old\" 2>/dev/null; true", installPath, installPath))

	// Install new binary
	res = u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("cp '%s' \"%s\" && chmod +x \"%s\"", binaryPath, installPath, installPath))
	if !res.Success {
		// Try to restore old binary
		u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("mv \"%s.old\" \"%s\" 2>/dev/null; true", installPath, installPath))
		u.transport.ExecuteUnsafe(ctx, "rm -rf '"+tmpDir+"'")
		return "", fmt.Errorf("install failed: %s", res.Stderr)
	}

	// Clean up
	u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("rm -f \"%s.old\"", installPath))
	u.transport.ExecuteUnsafe(ctx, "rm -rf '"+tmpDir+"'")

	// Verify new version
	newVersion := u.getCurrentVersion(ctx, b.Name, installPath)

	return fmt.Sprintf("Updated %s: %s -> %s\n  Installed to: %s", b.Name, b.CurrentVersion, newVersion, installPath), nil
}

// buildCurlCmd creates a curl command, passing token via env var to avoid process table leaks.
func (u *UpdatesCollector) buildCurlCmd(outputPath, url string) string {
	if u.cfg.GitHubToken != "" {
		return fmt.Sprintf(
			"GITHUB_TOKEN=%s curl -sfL -H 'Authorization: Bearer '\"$GITHUB_TOKEN\" -o '%s' '%s'",
			SanitizeForShell(u.cfg.GitHubToken), outputPath, url,
		)
	}
	return fmt.Sprintf("curl -sfL -o '%s' '%s'", outputPath, url)
}

// extractBinary extracts a binary from an archive or returns the path if it's a raw binary.
func (u *UpdatesCollector) extractBinary(ctx context.Context, downloadPath, tmpDir, binaryName string) (string, error) {
	lower := strings.ToLower(downloadPath)

	switch {
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		res := u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("tar xzf '%s' -C '%s'", downloadPath, tmpDir))
		if !res.Success {
			return "", fmt.Errorf("tar extraction failed: %s", res.Stderr)
		}
		return u.findExtractedBinary(ctx, tmpDir, binaryName)

	case strings.HasSuffix(lower, ".tar.xz"):
		res := u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("tar xJf '%s' -C '%s'", downloadPath, tmpDir))
		if !res.Success {
			return "", fmt.Errorf("tar extraction failed: %s", res.Stderr)
		}
		return u.findExtractedBinary(ctx, tmpDir, binaryName)

	case strings.HasSuffix(lower, ".zip"):
		res := u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("unzip -o '%s' -d '%s'", downloadPath, tmpDir))
		if !res.Success {
			return "", fmt.Errorf("zip extraction failed: %s", res.Stderr)
		}
		return u.findExtractedBinary(ctx, tmpDir, binaryName)

	case strings.HasSuffix(lower, ".gz") && !strings.HasSuffix(lower, ".tar.gz"):
		outPath := tmpDir + "/" + binaryName
		res := u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("gunzip -c '%s' > '%s' && chmod +x '%s'", downloadPath, outPath, outPath))
		if !res.Success {
			return "", fmt.Errorf("gunzip failed: %s", res.Stderr)
		}
		return outPath, nil

	default:
		// Assume raw binary
		res := u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("chmod +x '%s'", downloadPath))
		if !res.Success {
			return "", fmt.Errorf("chmod failed: %s", res.Stderr)
		}
		return downloadPath, nil
	}
}

// findExtractedBinary searches for the binary in extracted files.
func (u *UpdatesCollector) findExtractedBinary(ctx context.Context, tmpDir, binaryName string) (string, error) {
	// Search for exact name match first, then executable files
	cmd := fmt.Sprintf(
		"find '%s' -name '%s' -type f 2>/dev/null | head -1",
		tmpDir, binaryName,
	)
	res := u.transport.ExecuteUnsafe(ctx, cmd)
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		return strings.TrimSpace(res.Stdout), nil
	}

	// Try common patterns: binary might be in a subdirectory
	cmd = fmt.Sprintf(
		"find '%s' -type f -executable 2>/dev/null | head -5",
		tmpDir,
	)
	res = u.transport.ExecuteUnsafe(ctx, cmd)
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
		// Prefer file matching binary name
		for _, line := range lines {
			base := line[strings.LastIndex(line, "/")+1:]
			if base == binaryName {
				return strings.TrimSpace(line), nil
			}
		}
		// Only use first executable if it's the only one
		if len(lines) == 1 {
			return strings.TrimSpace(lines[0]), nil
		}
		return "", fmt.Errorf("multiple executables found, cannot determine which is %q", binaryName)
	}

	return "", fmt.Errorf("binary %q not found in extracted archive", binaryName)
}

// pickAsset selects the best matching release asset for the current platform.
func (u *UpdatesCollector) pickAsset(assets []githubAsset, binaryName string) *githubAsset {
	osPatterns := platformOSPatterns(u.detectOS())
	archPatterns := platformArchPatterns(u.detectArch())

	type scored struct {
		asset *githubAsset
		score int
	}
	var candidates []scored

	for i := range assets {
		a := &assets[i]
		name := strings.ToLower(a.Name)

		if isSkippableAsset(name) {
			continue
		}
		if !matchesPlatform(name, osPatterns, archPatterns) {
			continue
		}

		score, skip := scoreAssetFormat(name, binaryName)
		if skip {
			continue
		}
		candidates = append(candidates, scored{asset: a, score: score})
	}

	if len(candidates) == 0 {
		return nil
	}

	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.score > best.score {
			best = c
		}
	}
	return best.asset
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
// Higher scores are preferred. skip=true means the asset is a package format we cannot install.
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
		// Skip OS-native package formats; raw binaries score 6.
		pkgSuffixes := []string{".deb", ".rpm", ".apk", ".msi", ".dmg", ".pkg"}
		for _, s := range pkgSuffixes {
			if strings.HasSuffix(name, s) {
				return 0, true
			}
		}
		score = 6
	}

	// Bonus for containing binary name
	if strings.Contains(name, strings.ToLower(binaryName)) {
		score += 2
	}
	// Penalty for musl (prefer glibc unless on Alpine)
	if strings.Contains(name, "musl") {
		score--
	}
	return score, false
}

// detectOS returns the OS of the target machine.
func (u *UpdatesCollector) detectOS() string {
	if u.cfg.IsLocal() {
		return runtime.GOOS
	}
	return "linux"
}

// detectArch returns the architecture of the target machine.
func (u *UpdatesCollector) detectArch() string {
	if u.cfg.IsLocal() {
		return runtime.GOARCH
	}
	res := u.transport.ExecuteUnsafe(context.Background(), "uname -m 2>/dev/null")
	if res.Success {
		switch strings.TrimSpace(res.Stdout) {
		case "x86_64":
			return archAMD64
		case "aarch64":
			return archARM64
		case "armv7l":
			return "arm"
		}
	}
	return archARM64
}

// platformOSPatterns returns lowercase OS name patterns for asset matching.
func platformOSPatterns(goos string) []string {
	switch goos {
	case "linux":
		return []string{"linux"}
	case "darwin":
		return []string{"darwin", "macos", "apple"}
	case "windows":
		return []string{"windows", "win64", "win32"}
	default:
		return []string{goos}
	}
}

// platformArchPatterns returns lowercase arch name patterns for asset matching.
func platformArchPatterns(goarch string) []string {
	switch goarch {
	case archAMD64:
		return []string{archAMD64, "x86_64", "x64"}
	case archARM64:
		return []string{archARM64, "aarch64"}
	case "arm":
		return []string{"armv7", "arm"}
	case "386":
		return []string{"386", "i386", "i686", "x86"}
	default:
		return []string{goarch}
	}
}

// extractVersion extracts a semver-like version from version command output.
func extractVersion(output string) string {
	output = strings.TrimSpace(output)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Look for version patterns in each word
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
	// Remove trailing comma, parentheses
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
		// Allow digits and pre-release suffixes like "0-rc1"
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

// FormatUpdatesCheck creates a human-readable update check report.
func FormatUpdatesCheck(binaries []TrackedBinary) string {
	if len(binaries) == 0 {
		return "No tracked binaries found.\nConfigure DOZOR_TRACKED_BINARIES or install known CLIs to ~/.local/bin/."
	}

	var b strings.Builder
	b.WriteString("Binary Updates Check\n")
	b.WriteString(strings.Repeat("=", 40))
	b.WriteString("\n\n")

	updates, ok, skipped, errors := 0, 0, 0, 0
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
			errors++
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
	if errors > 0 {
		parts = append(parts, fmt.Sprintf("%d error(s)", errors))
	}
	fmt.Fprintf(&b, "Summary: %s\n", strings.Join(parts, ", "))

	return b.String()
}
