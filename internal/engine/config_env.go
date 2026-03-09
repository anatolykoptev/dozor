package engine

import (
	"os"
	"strconv"
	"strings"
	"time"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return strings.ToLower(v) == "true" || v == "1"
}

func envList(key, def string) []string {
	v := env(key, def)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// envDuration parses seconds from env.
func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return def
}

// envDurationStr parses Go duration string or "4h" from env.
func envDurationStr(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// expandHome replaces leading ~/ with user's home directory.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return path
}

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
