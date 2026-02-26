package engine

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// httpServerErrorMin is the minimum HTTP status code for a server error.
	httpServerErrorMin = 500
	// remoteSSLWarnHours is the number of hours before SSL expiry to warn.
	remoteSSLWarnHours = 14 * 24
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
				Description:     cfg.RemoteURL + " is not responding to HTTP requests",
				SuggestedAction: "Check nginx and upstream services on the remote server",
				Timestamp:       now,
			})
		} else if httpStatus >= httpServerErrorMin {
			status.Alerts = append(status.Alerts, Alert{
				Level:           AlertError,
				Service:         cfg.RemoteURL,
				Title:           fmt.Sprintf("HTTP %d error", httpStatus),
				Description:     fmt.Sprintf("%s returned status %d", cfg.RemoteURL, httpStatus),
				SuggestedAction: "Check application logs on the remote server",
				Timestamp:       now,
			})
		}

		if sslExpiry != nil && time.Until(*sslExpiry) < remoteSSLWarnHours*time.Hour {
			status.Alerts = append(status.Alerts, Alert{
				Level:           AlertWarning,
				Service:         cfg.RemoteURL,
				Title:           "SSL certificate expiring soon",
				Description:     "SSL certificate expires on " + sslExpiry.Format("2006-01-02"),
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
			res := t.ExecuteUnsafe(ctx, fmt.Sprintf("sudo systemctl is-active %s 2>/dev/null", svc))
			state := strings.TrimSpace(res.Stdout)
			if state == "" {
				state = string(StateUnknown)
			}
			status.Services[svc] = state

			if state != stateActive {
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
			state = string(StateUnknown)
		}

		icon := "OK"
		if state != stateActive {
			icon = "!!"
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", icon, svc, state)

		// Get uptime and memory
		res = t.ExecuteUnsafe(ctx, fmt.Sprintf("sudo systemctl show %s --property=ActiveEnterTimestamp,MemoryCurrent 2>/dev/null", svc))
		FormatSystemctlProperties(res.Stdout, &b)
	}

	return b.String()
}

// RemoteRestart restarts a service on the remote server and verifies it's active.
func RemoteRestart(ctx context.Context, cfg Config, service string) string {
	t := newRemoteTransport(cfg)

	res := t.ExecuteUnsafe(ctx, "sudo systemctl restart "+service)
	if !res.Success {
		return fmt.Sprintf("Failed to restart %s: %s", service, res.Output())
	}

	// Verify state after restart
	res = t.ExecuteUnsafe(ctx, fmt.Sprintf("sudo systemctl is-active %s 2>/dev/null", service))
	state := strings.TrimSpace(res.Stdout)
	if state == stateActive {
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
		return "No logs found for " + service
	}
	return fmt.Sprintf("Logs for %s on %s (last %d lines):\n\n%s", service, cfg.RemoteHost, lines, output)
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

// FormatRemoteAlerts formats critical/error alerts from a remote check for direct Telegram notification.
// Returns empty string if there are no actionable alerts.
func FormatRemoteAlerts(status *RemoteServerStatus) string {
	if status == nil || len(status.Alerts) == 0 {
		return ""
	}

	var b strings.Builder
	var count int
	for _, a := range status.Alerts {
		if a.Level != AlertCritical && a.Level != AlertError {
			continue
		}
		count++
	}
	if count == 0 {
		return ""
	}

	fmt.Fprintf(&b, "🚨 Remote server alert — %s\n\n", status.Host)
	for _, a := range status.Alerts {
		if a.Level != AlertCritical && a.Level != AlertError {
			continue
		}
		icon := "❌"
		if a.Level == AlertCritical {
			icon = "🔴"
		}
		fmt.Fprintf(&b, "%s %s: %s\n", icon, a.Title, a.Description)
		if a.SuggestedAction != "" {
			fmt.Fprintf(&b, "   → %s\n", a.SuggestedAction)
		}
	}
	return b.String()
}

func checkHTTP(ctx context.Context, url string) (int, *time.Time) {
	client := newHTTPClient(10 * time.Second)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
