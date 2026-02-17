package engine

import (
	"context"
	"fmt"
	"strings"
)

// AnalyzeAll runs error pattern analysis on all services, returning only those with issues.
func (a *ServerAgent) AnalyzeAll(ctx context.Context) string {
	services := a.resolveServices(ctx, nil)
	if len(services) == 0 {
		return "No services found."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Log Analysis — All Services\n%s\n\n", strings.Repeat("=", 40))

	var clean []string
	found := false
	for _, svc := range services {
		result := a.AnalyzeLogs(ctx, svc)
		if len(result.Issues) == 0 && result.ErrorCount == 0 {
			clean = append(clean, svc)
			continue
		}
		found = true
		fmt.Fprintf(&b, "%s: %d errors, %d warnings\n", result.Service, result.ErrorCount, result.WarningCount)
		for _, issue := range result.Issues {
			fmt.Fprintf(&b, "  [%s] %s (%d occurrences)\n", issue.Level, issue.Description, issue.Count)
			fmt.Fprintf(&b, "    Action: %s\n", issue.Action)
		}
		b.WriteString("\n")
	}

	if !found {
		b.WriteString("No error patterns detected in any service.\n")
	}
	if len(clean) > 0 {
		fmt.Fprintf(&b, "Clean services: %s\n", strings.Join(clean, ", "))
	}

	return b.String()
}

// GetAllErrors collects ERROR/FATAL log lines from all services.
func (a *ServerAgent) GetAllErrors(ctx context.Context) string {
	services := a.resolveServices(ctx, nil)
	if len(services) == 0 {
		return "No services found."
	}

	var b strings.Builder
	b.WriteString("Errors across all services (last 100 lines each):\n\n")

	var clean []string
	found := false
	for _, svc := range services {
		errors := a.logs.GetErrorLogs(ctx, svc, 100)
		if len(errors) == 0 {
			clean = append(clean, svc)
			continue
		}
		found = true

		// Cap at 20 lines per service
		shown := errors
		if len(shown) > 20 {
			shown = shown[len(shown)-20:]
		}

		fmt.Fprintf(&b, "%s (%d errors):\n", svc, len(errors))
		for _, e := range shown {
			ts := ""
			if e.Timestamp != nil {
				ts = e.Timestamp.Format("15:04:05")
			}
			msg := e.Message
			if len(msg) > 200 {
				msg = msg[:200] + "..."
			}
			fmt.Fprintf(&b, "  [%s] %s\n", ts, msg)
		}
		if len(errors) > 20 {
			fmt.Fprintf(&b, "  ... and %d more\n", len(errors)-20)
		}
		b.WriteString("\n")
	}

	if !found {
		b.WriteString("No errors found in any service.\n")
	}
	if len(clean) > 0 {
		fmt.Fprintf(&b, "Clean services: %s\n", strings.Join(clean, ", "))
	}

	return b.String()
}
