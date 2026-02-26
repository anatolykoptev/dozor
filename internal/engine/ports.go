package engine

import (
	"context"
	"fmt"
	"strings"
)

const (
	// ssFieldsMin is the minimum number of fields expected in an ss output line.
	ssFieldsMin = 5
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
	if len(fields) < ssFieldsMin {
		return nil
	}

	proto, ok := ssNetProto(fields[0])
	if !ok {
		return nil
	}

	// Local address is field 4 (0-indexed)
	addr, port := splitHostPort(fields[4])
	if port == "" || port == "*" || port == "0" {
		return nil
	}

	return &PortInfo{
		Port:     port,
		Protocol: proto,
		BindAddr: addr,
		Process:  ssExtractProcess(fields[5:]),
		Exposed:  addr == "0.0.0.0" || addr == "::" || addr == "*",
	}
}

// ssNetProto maps the netid field to a protocol string, returning false if not TCP/UDP.
func ssNetProto(netid string) (string, bool) {
	switch strings.ToLower(netid) {
	case "tcp", "tcp6":
		return "TCP", true
	case "udp", "udp6":
		return "UDP", true
	default:
		return "", false
	}
}

// ssExtractProcess extracts the process name from ss users:... fields.
func ssExtractProcess(fields []string) string {
	for _, f := range fields {
		if !strings.HasPrefix(f, "users:") {
			continue
		}
		// users:(("nginx",pid=123,fd=6))
		start := strings.Index(f, "((\"")
		end := strings.Index(f, "\",")
		if start >= 0 && end > start {
			return f[start+3 : end]
		}
		break
	}
	return ""
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
				proc = string(StateUnknown)
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
				proc = string(StateUnknown)
			}
			fmt.Fprintf(&b, "  [OK] %s/%s  addr=%s  process=%s\n", p.Port, p.Protocol, p.BindAddr, proc)
		}
		b.WriteString("\n")
	}

	return b.String()
}
