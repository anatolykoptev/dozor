package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// runCheck runs a one-shot diagnostics report and exits.
func runCheck(_ engine.Config, eng *engine.ServerAgent) {
	ctx := context.Background()
	asJSON := hasFlag("--json")

	var services []string
	if s := getFlagValue("--services"); s != "" {
		services = strings.Split(s, ",")
	}

	report := eng.Diagnose(ctx, services)

	if asJSON {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Print(engine.FormatReport(report))
	}

	if report.NeedsAttention() {
		os.Exit(1)
	}
}
