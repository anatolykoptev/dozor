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

// remoteProbeToken is echoed by the SSH reachability probe; the host counts as
// reachable only when it comes back verbatim.
const remoteProbeToken = "dozor-ssh-ok"

type (
	remoteExecFunc func(ctx context.Context, cmd string) CommandResult
	remoteHTTPFunc func(ctx context.Context, url string) (int, *time.Time)
)

// CheckRemoteServer monitors a remote server via HTTP and SSH.
func CheckRemoteServer(ctx context.Context, cfg Config) *RemoteServerStatus {
	if cfg.RemoteHost == "" && cfg.RemoteURL == "" {
		return nil
	}
	return checkRemote(ctx, cfg, newRemoteTransport(cfg).ExecuteUnsafe, checkHTTP)
}

// probeRemoteSSH reports whether the remote host answers over SSH. The second
// return value is the first stderr line (empty if none) for the alert text.
func probeRemoteSSH(ctx context.Context, exec remoteExecFunc) (bool, string) {
	res := exec(ctx, "echo "+remoteProbeToken)
	if res.ReturnCode == 0 && strings.TrimSpace(res.Stdout) == remoteProbeToken {
		return true, ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(res.Stderr), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		line = fmt.Sprintf("no valid response (exit code %d)", res.ReturnCode)
	}
	return false, line
}

func checkRemote(ctx context.Context, cfg Config, exec remoteExecFunc, httpCheck remoteHTTPFunc) *RemoteServerStatus {
	status := &RemoteServerStatus{
		Host:     cfg.RemoteHost,
		Services: make(map[string]string),
	}

	// Probe SSH once up front so a transport failure is not reported as N service failures.
	sshOK, sshErr := true, ""
	if cfg.RemoteHost != "" {
		sshOK, sshErr = probeRemoteSSH(ctx, exec)
	}

	// HTTP availability check
	if cfg.RemoteURL != "" {
		httpStatus, sslExpiry := httpCheck(ctx, cfg.RemoteURL)
		status.HTTPStatus = httpStatus
		status.SSLExpiry = sslExpiry
		now := time.Now()
		switch {
		case httpStatus == 0 && !sshOK:
			status.Alerts = append(status.Alerts, Alert{
				Level:   AlertCritical,
				Service: cfg.RemoteHost,
				Title:   "Remote host unreachable from the monitor (HTTP and SSH both fail)",
				Description: fmt.Sprintf("%s does not answer HTTP and SSH from this monitor (%s). "+
					"This is either a full outage or a network-path problem between the monitor and the host. "+
					"Service states are unknown, not failed: %s",
					cfg.RemoteURL, sshErr, strings.Join(cfg.RemoteServices, ", ")),
				SuggestedAction: "Confirm from another network before restarting anything",
				Timestamp:       now,
			})
		case httpStatus == 0:
			status.Alerts = append(status.Alerts, Alert{
				Level:           AlertCritical,
				Service:         cfg.RemoteURL,
				Title:           "Site unreachable",
				Description:     cfg.RemoteURL + " is not responding to HTTP requests",
				SuggestedAction: "Check nginx and upstream services on the remote server",
				Timestamp:       now,
			})
		case httpStatus >= httpServerErrorMin:
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
	if cfg.RemoteHost == "" {
		return status
	}
	if !sshOK {
		for _, svc := range cfg.RemoteServices {
			status.Services[svc] = string(StateUnknown)
		}
		// The combined HTTP+SSH alert above already covers this case.
		if status.HTTPStatus != 0 || cfg.RemoteURL == "" {
			status.Alerts = append(status.Alerts, Alert{
				Level:   AlertError,
				Service: cfg.RemoteHost,
				Title:   fmt.Sprintf("Remote host %s unreachable over SSH from the monitor", cfg.RemoteHost),
				Description: fmt.Sprintf("SSH failed from this monitor: %s. "+
					"Services were not checked; their state is UNKNOWN, not failed: %s",
					sshErr, strings.Join(cfg.RemoteServices, ", ")),
				SuggestedAction: "Check the network path from the monitor to the host; services are not known to be down",
				Timestamp:       time.Now(),
			})
		}
		return status
	}

	// Check systemd services
	for _, svc := range cfg.RemoteServices {
		res := exec(ctx, fmt.Sprintf("%s is-active %s 2>/dev/null", cfg.remoteSystemctl(), svc))
		state := strings.TrimSpace(res.Stdout)
		if state == "" {
			status.Services[svc] = string(StateUnknown)
			status.Alerts = append(status.Alerts, Alert{
				Level:           AlertError,
				Service:         svc,
				Title:           fmt.Sprintf("Remote service %s state unknown", svc),
				Description:     fmt.Sprintf("systemctl returned no state for %s on %s (exit code %d)", svc, cfg.RemoteHost, res.ReturnCode),
				SuggestedAction: fmt.Sprintf("SSH into %s and check: systemctl status %s", cfg.RemoteHost, svc),
				Timestamp:       time.Now(),
			})
			continue
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
	res := exec(ctx, "df -h / | tail -1")
	status.DiskUsage = strings.TrimSpace(res.Stdout)
	// Load average
	res = exec(ctx, "cat /proc/loadavg 2>/dev/null")
	status.LoadAvg = strings.TrimSpace(res.Stdout)
	return status
}

// remoteSystemctl returns the configured remote systemctl command prefix, or the
// plain `sudo systemctl` when unset (zero-value Config in tests and old callers).
func (c Config) remoteSystemctl() string {
	if c.RemoteSystemctl == "" {
		return defaultRemoteSystemctl
	}
	return c.RemoteSystemctl
}

// remoteJournalctl is remoteSystemctl's journal counterpart.
func (c Config) remoteJournalctl() string {
	if c.RemoteJournalctl == "" {
		return defaultRemoteJournalctl
	}
	return c.RemoteJournalctl
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
	return remoteServiceStatus(ctx, cfg, newRemoteTransport(cfg).ExecuteUnsafe)
}

func remoteServiceStatus(ctx context.Context, cfg Config, exec remoteExecFunc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Remote Services [%s] (%d)\n\n", cfg.RemoteHost, len(cfg.RemoteServices))
	if ok, sshErr := probeRemoteSSH(ctx, exec); !ok {
		fmt.Fprintf(&b, "SSH unreachable from monitor: %s — service states unknown\n", sshErr)
		return b.String()
	}
	for _, svc := range cfg.RemoteServices {
		res := exec(ctx, fmt.Sprintf("%s is-active %s 2>/dev/null", cfg.remoteSystemctl(), svc))
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
		res = exec(ctx, fmt.Sprintf("%s show %s --property=ActiveEnterTimestamp,MemoryCurrent 2>/dev/null", cfg.remoteSystemctl(), svc))
		FormatSystemctlProperties(res.Stdout, &b)
	}

	return b.String()
}

// RemoteRestart restarts a service on the remote server and verifies it's active.
func RemoteRestart(ctx context.Context, cfg Config, service string) string {
	return remoteRestart(ctx, cfg, service, newRemoteTransport(cfg).ExecuteUnsafe)
}

func remoteRestart(ctx context.Context, cfg Config, service string, exec remoteExecFunc) string {
	if ok, reason := guardUnit("remote_restart", service); !ok {
		return "Invalid service: " + reason
	}

	res := exec(ctx, cfg.remoteSystemctl()+" restart "+service)
	if !res.Success {
		return fmt.Sprintf("Failed to restart %s: %s", service, res.Output())
	}

	// Verify state after restart
	res = exec(ctx, fmt.Sprintf("%s is-active %s 2>/dev/null", cfg.remoteSystemctl(), service))
	state := strings.TrimSpace(res.Stdout)
	if state == stateActive {
		return fmt.Sprintf("Service %s restarted successfully (active).", service)
	}
	return fmt.Sprintf("Service %s restarted but state is: %s", service, state)
}

// RemoteLogs returns recent journal logs for a remote service.
func RemoteLogs(ctx context.Context, cfg Config, service string, lines int) string {
	return remoteLogs(ctx, cfg, service, lines, newRemoteTransport(cfg).ExecuteUnsafe)
}

func remoteLogs(ctx context.Context, cfg Config, service string, lines int, exec remoteExecFunc) string {
	if ok, reason := guardUnit("remote_logs", service); !ok {
		return "Invalid service: " + reason
	}

	res := exec(ctx, fmt.Sprintf("%s -u %s --no-pager -n %d", cfg.remoteJournalctl(), service, lines))
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

// RemoteAlertsHash returns a dedup key for the critical/error alerts of a remote
// check, built from alert identity (level, service, title) only. Descriptions
// carry volatile text (ssh stderr) and must not influence it.
func RemoteAlertsHash(status *RemoteServerStatus) string {
	var actionable []Alert
	if status != nil {
		for _, a := range status.Alerts {
			if a.Level == AlertCritical || a.Level == AlertError {
				actionable = append(actionable, a)
			}
		}
	}
	return hashAlerts(actionable)
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

	resp, err := client.Do(req) //nolint:gosec // request via external configured URL
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
