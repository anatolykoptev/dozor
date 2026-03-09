package engine

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
)

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
		if isSkippableAsset(name) || !matchesPlatform(name, osPatterns, archPatterns) {
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
	res := u.transport.ExecuteUnsafe(ctx, "mktemp -d ${TMPDIR:-/tmp}/dozor-update-XXXXXXXXXX")
	if !res.Success {
		return "", fmt.Errorf("failed to create temp dir: %s", res.Stderr)
	}
	tmpDir := strings.TrimSpace(res.Stdout)
	if tmpDir == "" {
		return "", errors.New("mktemp returned empty path")
	}
	downloadPath := tmpDir + "/" + asset.Name
	cmd := u.buildCurlCmd(downloadPath, asset.BrowserDownloadURL)
	res = u.transport.ExecuteUnsafe(ctx, cmd)
	if !res.Success {
		u.transport.ExecuteUnsafe(ctx, "rm -rf '"+tmpDir+"'")
		return "", fmt.Errorf("download failed: %s", res.Stderr)
	}
	binaryPath, err := u.extractBinary(ctx, downloadPath, tmpDir, b.Name)
	if err != nil {
		u.transport.ExecuteUnsafe(ctx, "rm -rf '"+tmpDir+"'")
		return "", fmt.Errorf("extraction failed: %w", err)
	}
	// Move old binary to avoid "text file busy"
	u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("mv \"%s\" \"%s.old\" 2>/dev/null; true", installPath, installPath))
	res = u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("cp '%s' \"%s\" && chmod +x \"%s\"", binaryPath, installPath, installPath))
	if !res.Success {
		u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("mv \"%s.old\" \"%s\" 2>/dev/null; true", installPath, installPath))
		u.transport.ExecuteUnsafe(ctx, "rm -rf '"+tmpDir+"'")
		return "", fmt.Errorf("install failed: %s", res.Stderr)
	}
	u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("rm -f \"%s.old\"", installPath))
	u.transport.ExecuteUnsafe(ctx, "rm -rf '"+tmpDir+"'")
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
		return u.extractArchive(ctx, fmt.Sprintf("tar xzf '%s' -C '%s'", downloadPath, tmpDir), tmpDir, binaryName)
	case strings.HasSuffix(lower, ".tar.xz"):
		return u.extractArchive(ctx, fmt.Sprintf("tar xJf '%s' -C '%s'", downloadPath, tmpDir), tmpDir, binaryName)
	case strings.HasSuffix(lower, ".zip"):
		return u.extractArchive(ctx, fmt.Sprintf("unzip -o '%s' -d '%s'", downloadPath, tmpDir), tmpDir, binaryName)
	case strings.HasSuffix(lower, ".gz") && !strings.HasSuffix(lower, ".tar.gz"):
		outPath := tmpDir + "/" + binaryName
		res := u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("gunzip -c '%s' > '%s' && chmod +x '%s'", downloadPath, outPath, outPath))
		if !res.Success {
			return "", fmt.Errorf("gunzip failed: %s", res.Stderr)
		}
		return outPath, nil
	default:
		res := u.transport.ExecuteUnsafe(ctx, fmt.Sprintf("chmod +x '%s'", downloadPath))
		if !res.Success {
			return "", fmt.Errorf("chmod failed: %s", res.Stderr)
		}
		return downloadPath, nil
	}
}

// extractArchive runs an extraction command and finds the binary.
func (u *UpdatesCollector) extractArchive(ctx context.Context, cmd, tmpDir, binaryName string) (string, error) {
	res := u.transport.ExecuteUnsafe(ctx, cmd)
	if !res.Success {
		return "", fmt.Errorf("extraction failed: %s", res.Stderr)
	}
	return u.findExtractedBinary(ctx, tmpDir, binaryName)
}

// findExtractedBinary searches for the binary in extracted files.
func (u *UpdatesCollector) findExtractedBinary(ctx context.Context, tmpDir, binaryName string) (string, error) {
	cmd := fmt.Sprintf("find '%s' -name '%s' -type f 2>/dev/null | head -1", tmpDir, binaryName)
	res := u.transport.ExecuteUnsafe(ctx, cmd)
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		return strings.TrimSpace(res.Stdout), nil
	}
	cmd = fmt.Sprintf("find '%s' -type f -executable 2>/dev/null | head -5", tmpDir)
	res = u.transport.ExecuteUnsafe(ctx, cmd)
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
		for _, line := range lines {
			base := line[strings.LastIndex(line, "/")+1:]
			if base == binaryName {
				return strings.TrimSpace(line), nil
			}
		}
		if len(lines) == 1 {
			return strings.TrimSpace(lines[0]), nil
		}
		return "", fmt.Errorf("multiple executables found, cannot determine which is %q", binaryName)
	}
	return "", fmt.Errorf("binary %q not found in extracted archive", binaryName)
}
