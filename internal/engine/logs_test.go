package engine

import (
	"testing"
)

func TestParseLogLines_DockerFormat(t *testing.T) {
	input := `2026-04-10T16:35:00.123Z ox-codes started
2026-04-10T16:35:01.456Z ERROR: memory limit exceeded
2026-04-10T16:35:02.789Z Killed`

	entries := parseLogLines(input, "ox-codes")
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[1].Level != "ERROR" {
		t.Errorf("expected ERROR level for 'ERROR: memory limit exceeded', got %s", entries[1].Level)
	}
	if entries[2].Level != "ERROR" {
		t.Errorf("expected ERROR for 'Killed', got %s", entries[2].Level)
	}
}

func TestParseLogLines_ServiceField(t *testing.T) {
	input := `2026-04-10T16:35:00.123Z starting up`

	entries := parseLogLines(input, "my-container")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Service != "my-container" {
		t.Errorf("expected service=my-container, got %s", entries[0].Service)
	}
}

func TestParseLogLines_Empty(t *testing.T) {
	entries := parseLogLines("", "svc")
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries for empty input, got %d", len(entries))
	}
}

func TestParseLogLines_DockerComposePrefix(t *testing.T) {
	// docker compose logs format: "service-name  | actual message"
	input := `go-code  | 2026-04-10T16:35:00.123Z server started`

	entries := parseLogLines(input, "go-code")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	// Message should strip the compose prefix
	if entries[0].Message == input {
		t.Errorf("expected compose prefix to be stripped, got full line: %s", entries[0].Message)
	}
}
