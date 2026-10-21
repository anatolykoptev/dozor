package engine

import (
	"context"
	"fmt"
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

func TestRemote_ProbeTokenMustMatchExactly(t *testing.T) {
	for _, stdout := range []string{"Welcome\n", "dozor-ssh-ok-extra", "Welcome\ndozor-ssh-ok"} {
		f := &fakeRemote{probe: CommandResult{Stdout: stdout, Success: true}, httpCode: 200}
		s := checkRemote(context.Background(), remoteCfg(), f.exec, f.http)
		if len(s.Alerts) != 1 || !strings.Contains(s.Alerts[0].Title, "unreachable over SSH") {
			t.Errorf("stdout %q: want 1 unreachable-over-SSH alert, got %d: %s", stdout, len(s.Alerts), titles(s))
		}
	}
}

func TestRemote_SSHDownHTTP500(t *testing.T) {
	f := &fakeRemote{probe: CommandResult{Stderr: sshTimeoutStderr, ReturnCode: 255}, httpCode: 500}
	s := checkRemote(context.Background(), remoteCfg(), f.exec, f.http)
	if len(s.Alerts) != 2 {
		t.Fatalf("want 2 alerts, got %d: %s", len(s.Alerts), titles(s))
	}
	if s.Alerts[0].Title != "HTTP 500 error" || !strings.Contains(s.Alerts[1].Title, "unreachable over SSH") {
		t.Errorf("unexpected alerts: %s", titles(s))
	}
}

func TestRemote_SSHDownNoURL(t *testing.T) {
	cfg := remoteCfg()
	cfg.RemoteURL = ""
	f := &fakeRemote{probe: CommandResult{Stderr: sshTimeoutStderr, ReturnCode: 255}}
	s := checkRemote(context.Background(), cfg, f.exec, f.http)
	if len(s.Alerts) != 1 || s.Alerts[0].Level != AlertError || !strings.Contains(s.Alerts[0].Title, "unreachable over SSH") {
		t.Fatalf("want 1 Error unreachable-over-SSH alert, got %d: %s", len(s.Alerts), titles(s))
	}
}

func TestRemote_AlertsHashIgnoresDescription(t *testing.T) {
	mk := func(title, desc string) *RemoteServerStatus {
		return &RemoteServerStatus{Alerts: []Alert{
			{Level: AlertError, Service: "h", Title: title, Description: desc},
			{Level: AlertWarning, Service: "u", Title: "ssl", Description: "warn"},
		}}
	}
	a := RemoteAlertsHash(mk("Remote host h unreachable", "SSH failed: Connection timed out"))
	b := RemoteAlertsHash(mk("Remote host h unreachable", "SSH failed: kex_exchange_identification: reset"))
	c := RemoteAlertsHash(mk("Remote host h other", "SSH failed: Connection timed out"))
	if a != b {
		t.Errorf("hash changed with Description only: %s vs %s", a, b)
	}
	if a == c {
		t.Errorf("hash equal despite different Title: %s", a)
	}
}

// recordingRemote answers every remote command with success and keeps the commands.
type recordingRemote struct{ cmds []string }

func (r *recordingRemote) exec(_ context.Context, cmd string) CommandResult {
	r.cmds = append(r.cmds, cmd)
	if strings.Contains(cmd, remoteProbeToken) {
		return okProbe()
	}
	return CommandResult{Stdout: "active", Success: true}
}

func (r *recordingRemote) joined() string { return strings.Join(r.cmds, "\n") }

func TestRemoteCommandPrefixDefaultsToSudoSystemctl(t *testing.T) {
	r := &recordingRemote{}
	cfg := remoteCfg()
	checkRemote(context.Background(), cfg, r.exec, (&fakeRemote{httpCode: 200}).http)
	remoteServiceStatus(context.Background(), cfg, r.exec)
	got := r.joined()
	if !strings.Contains(got, "sudo systemctl is-active mariadb 2>/dev/null") {
		t.Errorf("default is-active command missing:\n%s", got)
	}
	if !strings.Contains(got, "sudo systemctl show mariadb --property=ActiveEnterTimestamp,MemoryCurrent") {
		t.Errorf("default show command missing:\n%s", got)
	}
}

func TestRemoteCommandPrefixIsConfigurable(t *testing.T) {
	const wrap = "sudo -n /usr/local/sbin/piter-ops-svc"
	r := &recordingRemote{}
	cfg := remoteCfg()
	cfg.RemoteSystemctl = wrap
	checkRemote(context.Background(), cfg, r.exec, (&fakeRemote{httpCode: 200}).http)
	remoteServiceStatus(context.Background(), cfg, r.exec)
	got := r.joined()
	for _, want := range []string{
		wrap + " is-active mariadb 2>/dev/null",
		wrap + " show mariadb --property=ActiveEnterTimestamp,MemoryCurrent",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// No call may bypass the configured prefix: that would need the wildcard
	// sudoers rule the wrapper exists to remove.
	if strings.Contains(got, "sudo systemctl") {
		t.Errorf("a remote command ignored RemoteSystemctl:\n%s", got)
	}
}

func TestRemoteRestartAndLogsUseConfiguredPrefixes(t *testing.T) {
	r := &recordingRemote{}
	cfg := Config{RemoteHost: "h", RemoteSystemctl: "sudo -n W-svc", RemoteJournalctl: "sudo -n W-jrn"}
	remoteRestart(context.Background(), cfg, "nginx", r.exec)
	remoteLogs(context.Background(), cfg, "nginx", 50, r.exec)
	got := r.joined()
	for _, want := range []string{
		"sudo -n W-svc restart nginx",
		"sudo -n W-svc is-active nginx 2>/dev/null",
		"sudo -n W-jrn -u nginx --no-pager -n 50",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sudo systemctl") || strings.Contains(got, "sudo journalctl") {
		t.Errorf("restart/logs ignored the configured prefix:\n%s", got)
	}
}

func TestRemoteRestartAndLogsDefaultPrefixes(t *testing.T) {
	r := &recordingRemote{}
	cfg := Config{RemoteHost: "h"}
	remoteRestart(context.Background(), cfg, "nginx", r.exec)
	remoteLogs(context.Background(), cfg, "nginx", 50, r.exec)
	got := r.joined()
	for _, want := range []string{"sudo systemctl restart nginx", "sudo journalctl -u nginx --no-pager -n 50"} {
		if !strings.Contains(got, want) {
			t.Errorf("default command %q missing in:\n%s", want, got)
		}
	}
}

func TestRemoteLogsClampsLineCountToWrapperLimit(t *testing.T) {
	for _, tc := range []struct{ asked, want int }{{5000, 2000}, {2001, 2000}, {2000, 2000}, {50, 50}} {
		r := &recordingRemote{}
		remoteLogs(context.Background(), Config{RemoteHost: "h"}, "nginx", tc.asked, r.exec)
		want := fmt.Sprintf("--no-pager -n %d", tc.want)
		if !strings.Contains(r.joined(), want) {
			t.Errorf("asked %d: command %q lacks %q", tc.asked, r.joined(), want)
		}
	}
}
