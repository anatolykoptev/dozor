package engine

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// certHoursPerDay converts hours to days for certificate expiry calculation.
	certHoursPerDay = 24
	// certCriticalDays is the days threshold for CRITICAL cert status.
	certCriticalDays = 7
)

// CertInfo describes a single TLS certificate found on the server.
type CertInfo struct {
	Domain  string
	Path    string
	Expiry  time.Time
	Days    int
	Issuer  string
	IsValid bool
}

// ScanCerts finds and parses TLS certificates from common locations.
func ScanCerts(ctx context.Context) []CertInfo {
	var certs []CertInfo

	searchDirs := []string{
		"/etc/letsencrypt/live",
		"/var/lib/caddy/.local/share/caddy/certificates",
		"/root/.local/share/caddy/certificates",
		os.ExpandEnv("$HOME/.local/share/caddy/certificates"),
		"/etc/nginx/ssl",
		"/etc/ssl/private",
		"/opt/certs",
	}

	for _, dir := range searchDirs {
		found := scanCertDir(dir)
		certs = append(certs, found...)
	}

	return deduplicateCerts(certs)
}

func scanCertDir(dir string) []CertInfo {
	var certs []CertInfo

	// Let's Encrypt structure: /etc/letsencrypt/live/<domain>/cert.pem
	entries, err := os.ReadDir(dir)
	if err != nil {
		return certs
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		domain := entry.Name()
		certPath := filepath.Join(dir, domain, "cert.pem")
		if _, err := os.Stat(certPath); err != nil {
			// Try fullchain.pem (Caddy, nginx)
			certPath = filepath.Join(dir, domain, "fullchain.pem")
			if _, err := os.Stat(certPath); err != nil {
				continue
			}
		}
		if info := parseCertFile(certPath, domain); info != nil {
			certs = append(certs, *info)
		}
	}

	// Also look for direct .pem / .crt files in the dir
	pemEntries, err := filepath.Glob(filepath.Join(dir, "*.pem"))
	if err == nil {
		for _, path := range pemEntries {
			base := filepath.Base(path)
			domain := strings.TrimSuffix(base, ".pem")
			if info := parseCertFile(path, domain); info != nil {
				certs = append(certs, *info)
			}
		}
	}

	return certs
}

func parseCertFile(path, domain string) *CertInfo {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}

	days := int(time.Until(cert.NotAfter).Hours() / certHoursPerDay)
	d := domain
	if len(cert.DNSNames) > 0 {
		d = cert.DNSNames[0]
	} else if cert.Subject.CommonName != "" {
		d = cert.Subject.CommonName
	}

	return &CertInfo{
		Domain:  d,
		Path:    path,
		Expiry:  cert.NotAfter,
		Days:    days,
		Issuer:  cert.Issuer.CommonName,
		IsValid: days > 0,
	}
}

func deduplicateCerts(certs []CertInfo) []CertInfo {
	seen := make(map[string]bool)
	var result []CertInfo
	for _, c := range certs {
		if !seen[c.Domain] {
			seen[c.Domain] = true
			result = append(result, c)
		}
	}
	return result
}

// FormatCerts formats certificate inventory for display.
func FormatCerts(certs []CertInfo, warnDays int) string {
	if len(certs) == 0 {
		return "No TLS certificates found in standard locations.\n" +
			"Checked: /etc/letsencrypt/live/, Caddy storage, /etc/nginx/ssl/\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "TLS Certificate Inventory (%d found)\n\n", len(certs))

	for _, c := range certs {
		var icon string
		switch {
		case c.Days < 0:
			icon = displayIconExpired
		case c.Days < certCriticalDays:
			icon = displayIconCritical
		case c.Days < warnDays:
			icon = displayIconWarning
		default:
			icon = "OK"
		}

		fmt.Fprintf(&b, "[%s] %s\n", icon, c.Domain)
		fmt.Fprintf(&b, "  Expires: %s (%d days)\n", c.Expiry.Format("2006-01-02"), c.Days)
		fmt.Fprintf(&b, "  Issuer:  %s\n", c.Issuer)
		fmt.Fprintf(&b, "  Path:    %s\n\n", c.Path)
	}

	return b.String()
}
