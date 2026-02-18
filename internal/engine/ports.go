package engine

import (
	"context"
	"fmt"
	"strings"
)

// PortInfo describes a listening port.
type PortInfo struct {
	Port     string
	Protocol string
	BindAddr string
	Process  string
	Exposed  bool // true if bound to 0.0.0.0 or ::
}

// ScanPorts returns all listening TCP and UDP ports.
func ScanPorts(ctx context.Context, transport *Transport) []PortInfo {
	var ports []PortInfo

	// Use ss -tlunp for TCP+UDP with process info
	res := transport.ExecuteUnsafe(ctx, "ss -tlunp 2>/dev/null || netstat -tlunp 2>/dev/null")
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		return ports
	}

	for _, line := range strings.Split(res.Stdout, "\n") {
		if port := parseSsLine(line); port != nil {
			ports = append(ports, *port)
		}
	}

	return ports
}

func parseSsLine(line string) *PortInfo {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "Netid") || strings.HasPrefix(line, "Proto") {
		return nil
	}

	fields := strings.Fields(line)
	// ss -tlunp output: Netid State Recv-Q Send-Q Local Address:Port Peer Address:Port Process
	// minimum 5 fields
	if len(fields) < 5 {
		return nil
	}

	netid := strings.ToLower(fields[0])
	if netid != "tcp" && netid != "udp" && netid != "tcp6" && netid != "udp6" {
		return nil
	}
	proto := "TCP"
	if strings.HasPrefix(netid, "udp") {
		proto = "UDP"
	}

	// Local address is field 4 (0-indexed)
	localAddr := fields[4]

	// Split host:port — handle IPv6 [::1]:80 format
	addr, port := splitHostPort(localAddr)

	if port == "" || port == "*" || port == "0" {
		return nil
	}

	exposed := addr == "0.0.0.0" || addr == "::" || addr == "*"

	// Process info is last field if starts with "users:"
	process := ""
	for _, f := range fields[5:] {
		if strings.HasPrefix(f, "users:") {
			// users:(("nginx",pid=123,fd=6))
			// Extract process name
			start := strings.Index(f, "((\"")
			end := strings.Index(f, "\",")
			if start >= 0 && end > start {
				process = f[start+3 : end]
			}
			break
		}
	}

	return &PortInfo{
		Port:     port,
		Protocol: proto,
		BindAddr: addr,
		Process:  process,
		Exposed:  exposed,
	}
}

func splitHostPort(addr string) (host, port string) {
	// IPv6: [::1]:80 or [::]:80
	if strings.HasPrefix(addr, "[") {
		end := strings.LastIndex(addr, "]")
		if end < 0 {
			return addr, ""
		}
		host = addr[1:end]
		rest := addr[end+1:]
		if strings.HasPrefix(rest, ":") {
			port = rest[1:]
		}
		return host, port
	}
	// IPv4 or *:80
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return addr, ""
	}
	return addr[:idx], addr[idx+1:]
}

// FormatPorts formats port audit results for display.
func FormatPorts(ports []PortInfo) string {
	if len(ports) == 0 {
		return "No listening ports found (requires ss or netstat).\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Network Port Audit (%d listening ports)\n\n", len(ports))

	// Group: exposed first, then internal
	var exposed, internal []PortInfo
	for _, p := range ports {
		if p.Exposed {
			exposed = append(exposed, p)
		} else {
			internal = append(internal, p)
		}
	}

	if len(exposed) > 0 {
		b.WriteString("Externally exposed (0.0.0.0 / ::):\n")
		for _, p := range exposed {
			proc := p.Process
			if proc == "" {
				proc = "unknown"
			}
			fmt.Fprintf(&b, "  [!!] %s/%s  addr=%s  process=%s\n", p.Port, p.Protocol, p.BindAddr, proc)
		}
		b.WriteString("\n")
	}

	if len(internal) > 0 {
		b.WriteString("Internal only (127.0.0.1 / lo):\n")
		for _, p := range internal {
			proc := p.Process
			if proc == "" {
				proc = "unknown"
			}
			fmt.Fprintf(&b, "  [OK] %s/%s  addr=%s  process=%s\n", p.Port, p.Protocol, p.BindAddr, proc)
		}
		b.WriteString("\n")
	}

	return b.String()
}
