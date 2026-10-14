package engine

import (
	"context"
	"strings"
	"testing"
	"time"
)

const sshTimeoutStderr = "ssh: connect to host h port 1987: Connection timed out\nsecond line"

type fakeRemote struct {
	probe    CommandResult
	svc      map[string]string // service -> is-active stdout
	cmds     []string
	httpCode int
}

func (f *fakeRemote) exec(_ context.Context, cmd string) CommandResult {
	f.cmds = append(f.cmds, cmd)
	if strings.Contains(cmd, remoteProbeToken) {
		return f.probe
	}
	if strings.Contains(cmd, "is-active") {
		for svc, out := range f.svc {
			if strings.Contains(cmd, " "+svc+" ") {
				return CommandResult{Stdout: out, Success: out == "active", ReturnCode: 0}
			}
		}
	}
	return CommandResult{Stdout: "x", Success: true}
}

func (f *fakeRemote) http(_ context.Context, _ string) (int, *time.Time) {
	return f.httpCode, nil
}

func remoteCfg() Config {
	return Config{
		RemoteHost:     "h",
		RemoteURL:      "https://example.test",
		RemoteServices: []string{"php-fpm", "mariadb", "redis"},
	}
}

func okProbe() CommandResult {
	return CommandResult{Stdout: remoteProbeToken + "\n", Success: true}
}

func titles(s *RemoteServerStatus) string {
	var out []string
	for _, a := range s.Alerts {
		out = append(out, a.Title)
	}
	return strings.Join(out, " | ")
}

func assertUnreachableSingle(t *testing.T, f *fakeRemote, s *RemoteServerStatus, level AlertLevel, titlePart string) {
	t.Helper()
	if len(s.Alerts) != 1 {
		t.Fatalf("want exactly 1 alert, got %d: %s", len(s.Alerts), titles(s))
	}
	a := s.Alerts[0]
	if a.Level != level {
		t.Errorf("alert level = %v, want %v", a.Level, level)
	}
	if !strings.Contains(a.Title, titlePart) {
		t.Errorf("title %q does not contain %q", a.Title, titlePart)
	}
	if strings.Contains(a.Title, "Site unreachable") || strings.Contains(a.Title, "Remote service") ||
		strings.Contains(a.Title, "is not active") {
		t.Errorf("unexpected title %q", a.Title)
	}
	for svc, st := range s.Services {
		if st != "unknown" {
			t.Errorf("Services[%s] = %q, want unknown", svc, st)
		}
	}
	if len(s.Services) != 3 {
		t.Errorf("Services has %d entries, want 3", len(s.Services))
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "systemctl") || strings.Contains(c, "df ") || strings.Contains(c, "loadavg") {
			t.Errorf("command run after failed SSH probe: %q", c)
		}
	}
}

func TestRemote_T1_SSHUnreachableHTTPOK(t *testing.T) {
	f := &fakeRemote{probe: CommandResult{Stderr: sshTimeoutStderr, ReturnCode: 255}, httpCode: 200}
	s := checkRemote(context.Background(), remoteCfg(), f.exec, f.http)
	assertUnreachableSingle(t, f, s, AlertError, "unreachable over SSH")
	d := s.Alerts[0].Description
	if !strings.Contains(d, "Connection timed out") || strings.Contains(d, "second line") {
		t.Errorf("description must carry only the first stderr line: %q", d)
	}
	if strings.Contains(d, "is not active") {
		t.Errorf("description must not claim services are not active: %q", d)
	}
}

func TestRemote_T2_SSHUnreachableHTTPZero(t *testing.T) {
	f := &fakeRemote{probe: CommandResult{Stderr: sshTimeoutStderr, ReturnCode: 255}, httpCode: 0}
	s := checkRemote(context.Background(), remoteCfg(), f.exec, f.http)
	assertUnreachableSingle(t, f, s, AlertCritical, "HTTP and SSH both fail")
}

func TestRemote_T3_SSHProbeTimeout(t *testing.T) {
	f := &fakeRemote{probe: CommandResult{Stderr: "timeout", ReturnCode: -1}, httpCode: 200}
	s := checkRemote(context.Background(), remoteCfg(), f.exec, f.http)
	assertUnreachableSingle(t, f, s, AlertError, "unreachable over SSH")
}

func TestRemote_T4_SSHProbeWrongToken(t *testing.T) {
	f := &fakeRemote{probe: CommandResult{Stdout: "", Success: true, ReturnCode: 0}, httpCode: 200}
	s := checkRemote(context.Background(), remoteCfg(), f.exec, f.http)
	assertUnreachableSingle(t, f, s, AlertError, "unreachable over SSH")
}

func TestRemote_T5_SSHOKHTTPZero(t *testing.T) {
	f := &fakeRemote{
		probe:    okProbe(),
		svc:      map[string]string{"php-fpm": "active", "mariadb": "active", "redis": "active"},
		httpCode: 0,
	}
	s := checkRemote(context.Background(), remoteCfg(), f.exec, f.http)
	if len(s.Alerts) != 1 || s.Alerts[0].Title != "Site unreachable" || s.Alerts[0].Level != AlertCritical {
		t.Fatalf("want exactly the Site unreachable alert, got %d: %s", len(s.Alerts), titles(s))
	}
}

func TestRemote_T6_ServiceStates(t *testing.T) {
	f := &fakeRemote{
		probe:    okProbe(),
		svc:      map[string]string{"php-fpm": "inactive", "mariadb": "", "redis": "active"},
		httpCode: 200,
	}
	s := checkRemote(context.Background(), remoteCfg(), f.exec, f.http)
	if len(s.Alerts) != 2 {
		t.Fatalf("want 2 alerts, got %d: %s", len(s.Alerts), titles(s))
	}
	var sawInactive, sawUnknown bool
	for _, a := range s.Alerts {
		switch a.Service {
		case "php-fpm":
			sawInactive = a.Title == "Remote service php-fpm is inactive"
			if !sawInactive {
				t.Errorf("php-fpm title = %q", a.Title)
			}
		case "mariadb":
			sawUnknown = strings.Contains(a.Title, "state unknown")
			if !sawUnknown {
				t.Errorf("mariadb title = %q, want it to contain 'state unknown'", a.Title)
			}
			if strings.Contains(a.Description, "is not active") {
				t.Errorf("unknown-state description must not say 'is not active': %q", a.Description)
			}
		}
	}
	if !sawInactive || !sawUnknown {
		t.Errorf("missing expected alerts: %s", titles(s))
	}
}

func TestRemote_T7_Healthy(t *testing.T) {
	f := &fakeRemote{
		probe:    okProbe(),
		svc:      map[string]string{"php-fpm": "active", "mariadb": "active", "redis": "active"},
		httpCode: 200,
	}
	s := checkRemote(context.Background(), remoteCfg(), f.exec, f.http)
	if len(s.Alerts) != 0 {
		t.Fatalf("want 0 alerts, got %d: %s", len(s.Alerts), titles(s))
	}
}

func TestRemote_ServiceStatusSSHUnreachable(t *testing.T) {
	f := &fakeRemote{probe: CommandResult{Stderr: sshTimeoutStderr, ReturnCode: 255}}
	out := remoteServiceStatus(context.Background(), remoteCfg(), f.exec)
	if !strings.Contains(out, "SSH unreachable from monitor: ssh: connect to host h port 1987: Connection timed out") {
		t.Errorf("missing unreachable line: %q", out)
	}
	if strings.Contains(out, "php-fpm") {
		t.Errorf("per-service rows must not be emitted: %q", out)
	}
}
