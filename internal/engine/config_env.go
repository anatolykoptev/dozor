package engine

import (
	"strconv"
	"strings"
	"time"

	kitenv "github.com/anatolykoptev/go-kit/env"
)

// Thin aliases over go-kit/env. Behaviour matches the prior in-process
// implementations exactly: kitenv.Bool accepts "true"/"1"/"yes"
// (superset of prior "true"/"1"); kitenv.Duration accepts Go duration
// strings AND float-seconds fallback, so it serves both envDuration
// (seconds-as-int) and envDurationStr (Go format) call sites.
func env(key, def string) string                                     { return kitenv.Str(key, def) }
func envInt(key string, def int) int                                 { return kitenv.Int(key, def) }
func envFloat(key string, def float64) float64                       { return kitenv.Float(key, def) }
func envBool(key string, def bool) bool                              { return kitenv.Bool(key, def) }
func envList(key, def string) []string                               { return kitenv.List(key, def) }
func envDuration(key string, def time.Duration) time.Duration        { return kitenv.Duration(key, def) }
func envDurationStr(key string, def time.Duration) time.Duration     { return kitenv.Duration(key, def) }

// parseTrackedBinaries parses "owner/repo:binary,owner/repo:binary" format.
func parseTrackedBinaries(raw string) []TrackedBinaryConfig {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	binaries := make([]TrackedBinaryConfig, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Split "owner/repo:binary" or "owner/repo" (binary = repo)
		var ownerRepo, binary string
		if idx := strings.Index(p, ":"); idx > 0 {
			ownerRepo = p[:idx]
			binary = p[idx+1:]
		} else {
			ownerRepo = p
		}
		slashIdx := strings.Index(ownerRepo, "/")
		if slashIdx <= 0 || slashIdx == len(ownerRepo)-1 {
			continue
		}
		owner := ownerRepo[:slashIdx]
		repo := ownerRepo[slashIdx+1:]
		if binary == "" {
			binary = repo
		}
		// Validate all parts
		if ok, _ := ValidateGitHubName(owner); !ok {
			continue
		}
		if ok, _ := ValidateGitHubName(repo); !ok {
			continue
		}
		if ok, _ := ValidateBinaryName(binary); !ok {
			continue
		}
		binaries = append(binaries, TrackedBinaryConfig{
			Owner:  owner,
			Repo:   repo,
			Binary: binary,
		})
	}
	return binaries
}

// parseUserServices parses "name:port,name:port" format.
func parseUserServices(raw string) []UserService {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	services := make([]UserService, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		svc := UserService{}
		if idx := strings.LastIndex(p, ":"); idx > 0 {
			svc.Name = strings.TrimSpace(p[:idx])
			if port, err := strconv.Atoi(strings.TrimSpace(p[idx+1:])); err == nil {
				svc.Port = port
			} else {
				svc.Name = p
			}
		} else {
			svc.Name = p
		}
		if svc.Name != "" {
			services = append(services, svc)
		}
	}
	return services
}

// parseMCPServers parses "id=url,id=url" format.
func parseMCPServers(raw string) map[string]MCPServerConfig {
	if raw == "" {
		return nil
	}
	servers := make(map[string]MCPServerConfig)
	parts := strings.Split(raw, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		idx := strings.Index(p, "=")
		if idx <= 0 {
			continue
		}
		id := strings.TrimSpace(p[:idx])
		url := strings.TrimSpace(p[idx+1:])
		if id != "" && url != "" {
			servers[id] = MCPServerConfig{URL: url, Alias: id}
		}
	}
	return servers
}
