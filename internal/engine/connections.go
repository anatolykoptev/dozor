package engine

import (
	"context"
	"fmt"
	"strings"
)

// GetConnections returns a network connections summary.
func (a *ServerAgent) GetConnections(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("Network Connections\n")
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	// TCP connections by state
	res := a.transport.ExecuteUnsafe(ctx, "ss -tn 2>/dev/null | awk 'NR>1{print $1}' | sort | uniq -c | sort -rn")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		b.WriteString("TCP by state:\n")
		for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			count := 0
			fmt.Sscanf(fields[0], "%d", &count)
			state := fields[1]
			flag := ""
			if (state == "CLOSE-WAIT" && count > 50) || (state == "TIME-WAIT" && count > 500) {
				flag = " !!"
			}
			fmt.Fprintf(&b, "  %-15s %d%s\n", state, count, flag)
		}
		b.WriteString("\n")
	}

	// UDP listeners
	res = a.transport.ExecuteUnsafe(ctx, "ss -ulnp 2>/dev/null | awk 'NR>1{print $4, $NF}'")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		b.WriteString("UDP listeners:\n")
		for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			if strings.TrimSpace(line) != "" {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
		b.WriteString("\n")
	}

	// Top 15 remote IPs by connection count
	res = a.transport.ExecuteUnsafe(ctx, "ss -tn state established 2>/dev/null | awk 'NR>1{split($4,a,\":\"); print a[1]}' | sort | uniq -c | sort -rn | head -15")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		b.WriteString("Top remote IPs (established):\n")
		for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				count := 0
				fmt.Sscanf(fields[0], "%d", &count)
				flag := ""
				if count > 100 {
					flag = " !!"
				}
				fmt.Fprintf(&b, "  %-20s %d connections%s\n", fields[1], count, flag)
			}
		}
		b.WriteString("\n")
	}

	// Per-listening-port established count
	res = a.transport.ExecuteUnsafe(ctx, "ss -tlnp 2>/dev/null | awk 'NR>1{print $4}'")
	if res.Success && strings.TrimSpace(res.Stdout) != "" {
		b.WriteString("Per-service established connections:\n")
		for _, addr := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			// Extract port from address
			port := addr
			if idx := strings.LastIndex(addr, ":"); idx >= 0 {
				port = addr[idx+1:]
			}
			portRes := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("ss -tn state established '( dport = :%s )' 2>/dev/null | wc -l", port))
			if portRes.Success {
				count := 0
				fmt.Sscanf(strings.TrimSpace(portRes.Stdout), "%d", &count)
				if count > 1 { // subtract header
					count--
				}
				if count > 0 {
					fmt.Fprintf(&b, "  :%s → %d established\n", port, count)
				}
			}
		}
	}

	return b.String()
}
