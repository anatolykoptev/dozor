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
		t := &Transport{cfg: Config{
			Host:    cfg.RemoteHost,
			SSHPort: 22,
			Timeout: cfg.Timeout,
		}}

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
