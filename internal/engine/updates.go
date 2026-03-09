package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	archAMD64 = "amd64"
	archARM64 = "arm64"
)

// UpdatesCollector checks for binary updates via GitHub releases.
type UpdatesCollector struct {
	transport *Transport
	cfg       Config
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
	target.CurrentVersion = u.getCurrentVersion(ctx, target.Name, target.Path)

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
			Name: cfg.Binary, Path: path, Owner: cfg.Owner, Repo: cfg.Repo,
		})
	}
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
			Name: name, Path: "$HOME/.local/bin/" + name, Owner: parts[0], Repo: parts[1],
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
	b.CurrentVersion = u.getCurrentVersion(ctx, b.Name, b.Path)
	if b.CurrentVersion == "" {
		b.Status = "SKIP"
		b.Error = "version unknown"
		return
	}
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
