package engine

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CheckRemoteServer monitors a remote server via HTTP and SSH.
func CheckRemoteServer(ctx context.Context, cfg Config) *RemoteServerStatus {
	if cfg.RemoteHost == "" && cfg.RemoteURL == "" {
		return nil
	}

	status := &RemoteServerStatus{
		Host:     cfg.RemoteHost,
		Services: make(map[string]string),
	}

	// HTTP availability check
	if cfg.RemoteURL != "" {
		httpStatus, sslExpiry := checkHTTP(ctx, cfg.RemoteURL)
		status.HTTPStatus = httpStatus
		status.SSLExpiry = sslExpiry

		now := time.Now()
		if httpStatus == 0 {
			status.Alerts = append(status.Alerts, Alert{
				Level:           AlertCritical,
				Service:         cfg.RemoteURL,
				Title:           "Site unreachable",
				Description:     fmt.Sprintf("%s is not responding to HTTP requests", cfg.RemoteURL),
				SuggestedAction: "Check nginx and upstream services on the remote server",
				Timestamp:       now,
			})
		} else if httpStatus >= 500 {
			status.Alerts = append(status.Alerts, Alert{
				Level:           AlertError,
				Service:         cfg.RemoteURL,
				Title:           fmt.Sprintf("HTTP %d error", httpStatus),
				Description:     fmt.Sprintf("%s returned status %d", cfg.RemoteURL, httpStatus),
				SuggestedAction: "Check application logs on the remote server",
				Timestamp:       now,
			})
		}

		if sslExpiry != nil && time.Until(*sslExpiry) < 14*24*time.Hour {
			status.Alerts = append(status.Alerts, Alert{
				Level:           AlertWarning,
				Service:         cfg.RemoteURL,
				Title:           "SSL certificate expiring soon",
				Description:     fmt.Sprintf("SSL certificate expires on %s", sslExpiry.Format("2006-01-02")),
				SuggestedAction: "Renew SSL certificate",
				Timestamp:       now,
			})
		}
	}

	// SSH-based checks
	if cfg.RemoteHost != "" {
		t := newRemoteTransport(cfg)

		// Check systemd services
		for _, svc := range cfg.RemoteServices {
			res := t.ExecuteUnsafe(ctx, fmt.Sprintf("systemctl is-active %s 2>/dev/null", svc))
			state := strings.TrimSpace(res.Stdout)
			if state == "" {
				state = "unknown"
			}
			status.Services[svc] = state

			if state != "active" {
				status.Alerts = append(status.Alerts, Alert{
					Level:           AlertError,
					Service:         svc,
					Title:           fmt.Sprintf("Remote service %s is %s", svc, state),
					Description:     fmt.Sprintf("Systemd service %s on %s is not active", svc, cfg.RemoteHost),
					SuggestedAction: fmt.Sprintf("SSH into %s and check: systemctl status %s", cfg.RemoteHost, svc),
					Timestamp:       time.Now(),
				})
			}
		}

		// Disk usage
		res := t.ExecuteUnsafe(ctx, "df -h / | tail -1")
		status.DiskUsage = strings.TrimSpace(res.Stdout)

		// Load average
		res = t.ExecuteUnsafe(ctx, "cat /proc/loadavg 2>/dev/null")
		status.LoadAvg = strings.TrimSpace(res.Stdout)
	}

	return status
}

// FormatRemoteStatus formats remote server status as a human-readable report.
func FormatRemoteStatus(s *RemoteServerStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Remote Server: %s\n", s.Host)
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	if s.HTTPStatus > 0 {
		icon := "OK"
		if s.HTTPStatus >= 400 {
			icon = "!!"
		}
		fmt.Fprintf(&b, "[%s] HTTP: %d\n", icon, s.HTTPStatus)
	}

	if s.SSLExpiry != nil {
		days := int(time.Until(*s.SSLExpiry).Hours() / 24)
		icon := "OK"
		if days < 14 {
			icon = "!!"
		}
		fmt.Fprintf(&b, "[%s] SSL expires: %s (%d days)\n", icon, s.SSLExpiry.Format("2006-01-02"), days)
	}

	if len(s.Services) > 0 {
		b.WriteString("\nServices:\n")
		for name, state := range s.Services {
			icon := "OK"
			if state != "active" {
				icon = "!!"
			}
			fmt.Fprintf(&b, "  [%s] %s: %s\n", icon, name, state)
		}
	}

	if s.DiskUsage != "" {
		fmt.Fprintf(&b, "\nDisk: %s\n", s.DiskUsage)
	}
	if s.LoadAvg != "" {
		fmt.Fprintf(&b, "Load: %s\n", s.LoadAvg)
	}

	if len(s.Alerts) > 0 {
		fmt.Fprintf(&b, "\nAlerts (%d):\n", len(s.Alerts))
		for _, a := range s.Alerts {
			fmt.Fprintf(&b, "  [%s] %s: %s\n", a.Level, a.Service, a.Title)
		}
	}

	return b.String()
}

// newRemoteTransport creates a Transport configured for the remote server.
func newRemoteTransport(cfg Config) *Transport {
	return NewTransport(Config{
		Host:    cfg.RemoteHost,
		SSHPort: cfg.RemoteSSHPort,
		Timeout: cfg.Timeout,
	})
}

// RemoteServiceStatus returns status of all configured remote services.
func RemoteServiceStatus(ctx context.Context, cfg Config) string {
	t := newRemoteTransport(cfg)

	var b strings.Builder
	fmt.Fprintf(&b, "Remote Services [%s] (%d)\n\n", cfg.RemoteHost, len(cfg.RemoteServices))

	for _, svc := range cfg.RemoteServices {
		res := t.ExecuteUnsafe(ctx, fmt.Sprintf("sudo systemctl is-active %s 2>/dev/null", svc))
		state := strings.TrimSpace(res.Stdout)
		if state == "" {
			state = "unknown"
		}

		icon := "OK"
		if state != "active" {
			icon = "!!"
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", icon, svc, state)

		// Get uptime and memory
		res = t.ExecuteUnsafe(ctx, fmt.Sprintf("sudo systemctl show %s --property=ActiveEnterTimestamp,MemoryCurrent 2>/dev/null", svc))
		for _, line := range strings.Split(res.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ActiveEnterTimestamp=") {
				ts := strings.TrimPrefix(line, "ActiveEnterTimestamp=")
				if ts != "" {
					fmt.Fprintf(&b, "  Started: %s\n", ts)
				}
			}
			if strings.HasPrefix(line, "MemoryCurrent=") {
				mem := strings.TrimPrefix(line, "MemoryCurrent=")
				if mem != "" && mem != "[not set]" && mem != "18446744073709551615" {
					if mb, ok := bytesToMBRemote(mem); ok {
						fmt.Fprintf(&b, "  Memory: %.1f MB\n", mb)
					}
				}
			}
		}
	}

	return b.String()
}

// RemoteRestart restarts a service on the remote server and verifies it's active.
func RemoteRestart(ctx context.Context, cfg Config, service string) string {
	t := newRemoteTransport(cfg)

	res := t.ExecuteUnsafe(ctx, fmt.Sprintf("sudo systemctl restart %s", service))
	if !res.Success {
		return fmt.Sprintf("Failed to restart %s: %s", service, res.Output())
	}

	// Verify state after restart
	res = t.ExecuteUnsafe(ctx, fmt.Sprintf("sudo systemctl is-active %s 2>/dev/null", service))
	state := strings.TrimSpace(res.Stdout)
	if state == "active" {
		return fmt.Sprintf("Service %s restarted successfully (active).", service)
	}
	return fmt.Sprintf("Service %s restarted but state is: %s", service, state)
}

// RemoteLogs returns recent journal logs for a remote service.
func RemoteLogs(ctx context.Context, cfg Config, service string, lines int) string {
	t := newRemoteTransport(cfg)

	res := t.ExecuteUnsafe(ctx, fmt.Sprintf("sudo journalctl -u %s --no-pager -n %d", service, lines))
	if !res.Success {
		return fmt.Sprintf("Failed to get logs for %s: %s", service, res.Output())
	}
	output := res.Output()
	if output == "" {
		return fmt.Sprintf("No logs found for %s", service)
	}
	return fmt.Sprintf("Logs for %s on %s (last %d lines):\n\n%s", service, cfg.RemoteHost, lines, output)
}

// bytesToMBRemote converts a byte count string to float64 MB.
func bytesToMBRemote(s string) (float64, bool) {
	var n int64
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int64(c-'0')
		} else {
			break
		}
	}
	if n <= 0 {
		return 0, false
	}
	return float64(n) / (1024 * 1024), true
}

// RemoteServiceNames returns the list of configured remote service names.
func RemoteServiceNames(cfg Config) []string {
	return cfg.RemoteServices
}

// IsValidRemoteService checks if a service name is in the configured remote services list.
func IsValidRemoteService(cfg Config, name string) bool {
	for _, svc := range cfg.RemoteServices {
		if svc == name {
			return true
		}
	}
	return false
}

func checkHTTP(ctx context.Context, url string) (int, *time.Time) {
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, nil
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()

	var sslExpiry *time.Time
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		expiry := resp.TLS.PeerCertificates[0].NotAfter
		sslExpiry = &expiry
	}

	return resp.StatusCode, sslExpiry
}
