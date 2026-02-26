package engine

import (
	"context"
	"fmt"
	"strings"
)

const (
	// connCloseWaitWarn is the CLOSE-WAIT count threshold for flagging.
	connCloseWaitWarn = 50
	// connTimeWaitWarn is the TIME-WAIT count threshold for flagging.
	connTimeWaitWarn = 500
	// connTopRemoteIPs is the maximum number of remote IPs to show.
	connTopRemoteIPs = 15
	// connIPHighWarn is the per-IP connection count threshold for flagging.
	connIPHighWarn = 100
)

// GetConnections returns a network connections summary.
func (a *ServerAgent) GetConnections(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("Network Connections\n")
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	writeTCPByState(ctx, &b, a)
	writeUDPListeners(ctx, &b, a)
	writeTopRemoteIPs(ctx, &b, a)
	writePerServiceConnections(ctx, &b, a)

	return b.String()
}

// writeTCPByState writes TCP connection counts grouped by state.
func writeTCPByState(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "ss -tn 2>/dev/null | awk 'NR>1{print $1}' | sort | uniq -c | sort -rn")
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		return
	}
	b.WriteString("TCP by state:\n")
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		count := 0
		_, _ = fmt.Sscanf(fields[0], "%d", &count)
		state := fields[1]
		flag := ""
		if (state == "CLOSE-WAIT" && count > connCloseWaitWarn) ||
			(state == "TIME-WAIT" && count > connTimeWaitWarn) {
			flag = " !!"
		}
		fmt.Fprintf(b, "  %-15s %d%s\n", state, count, flag)
	}
	b.WriteString("\n")
}

// writeUDPListeners writes UDP listening ports.
func writeUDPListeners(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "ss -ulnp 2>/dev/null | awk 'NR>1{print $4, $NF}'")
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		return
	}
	b.WriteString("UDP listeners:\n")
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if strings.TrimSpace(line) != "" {
			fmt.Fprintf(b, "  %s\n", line)
		}
	}
	b.WriteString("\n")
}

// writeTopRemoteIPs writes the top remote IPs by established connection count.
func writeTopRemoteIPs(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx,
		fmt.Sprintf("ss -tn state established 2>/dev/null | awk 'NR>1{split($4,a,\":\"); print a[1]}' | sort | uniq -c | sort -rn | head -%d",
			connTopRemoteIPs))
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		return
	}
	b.WriteString("Top remote IPs (established):\n")
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		count := 0
		_, _ = fmt.Sscanf(fields[0], "%d", &count)
		flag := ""
		if count > connIPHighWarn {
			flag = " !!"
		}
		fmt.Fprintf(b, "  %-20s %d connections%s\n", fields[1], count, flag)
	}
	b.WriteString("\n")
}

// writePerServiceConnections writes established connection counts per listening port.
func writePerServiceConnections(ctx context.Context, b *strings.Builder, a *ServerAgent) {
	res := a.transport.ExecuteUnsafe(ctx, "ss -tlnp 2>/dev/null | awk 'NR>1{print $4}'")
	if !res.Success || strings.TrimSpace(res.Stdout) == "" {
		return
	}
	b.WriteString("Per-service established connections:\n")
	for _, addr := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		port := addr
		if idx := strings.LastIndex(addr, ":"); idx >= 0 {
			port = addr[idx+1:]
		}
		portRes := a.transport.ExecuteUnsafe(ctx, fmt.Sprintf("ss -tn state established '( dport = :%s )' 2>/dev/null | wc -l", port))
		if !portRes.Success {
			continue
		}
		count := 0
		_, _ = fmt.Sscanf(strings.TrimSpace(portRes.Stdout), "%d", &count)
		if count > 1 { // subtract header
			count--
		}
		if count > 0 {
			fmt.Fprintf(b, "  :%s → %d established\n", port, count)
		}
	}
}
