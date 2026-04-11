package engine

import (
	"strings"
	"testing"
)

func TestClassifyProcess_UserSession(t *testing.T) {
	cases := []string{
		"krolik 3188799 13.9 2.5 75420136 627292 pts/4 Rl+ 01:18 18:32 claude --dangerously-skip-permissions",
		"krolik 3059463 12.5 2.5 75485672 627148 pts/6 Sl+ 00:51 20:06 claude-code",
		"krolik 12345  1.0 0.5 100000 5000 pts/1 Sl 00:00 0:01 /home/krolik/.local/bin/windsurf-server --port 12345",
		"krolik 99999  5.0 0.3 50000 3000 pts/2 R+ 00:00 0:00 code-review-graph update",
		"krolik 88888  2.0 0.2 40000 2000 pts/3 S 00:00 0:00 cursor-server --start",
	}
	for _, line := range cases {
		if got := classifyProcess(line); got != tagUserSession {
			t.Errorf("expected %s for %q, got %q", tagUserSession, line, got)
		}
	}
}

func TestClassifyProcess_Build(t *testing.T) {
	cases := []string{
		"root 4007753 30.9 0.6 1418096 164184 ? Rl 02:55 0:03 /usr/local/go/pkg/tool/linux_arm64/compile -o /tmp/go-build458127477/b009/_pkg_.a -trimpath runtime",
		"krolik 1234 50.0 5.0 999999 999999 ? Rl 00:00 1:00 cargo build --release",
		"krolik 2345 40.0 3.0 500000 500000 ? Rl 00:00 0:30 rustc --edition=2021 -Copt-level=3 src/lib.rs",
		"krolik 3456  5.0 1.0 100000 100000 ? Sl 00:00 0:05 docker compose build cloakbrowser",
		"krolik 4567  2.0 0.1 10000  10000  ? R+ 00:00 0:00 du -h -d1 /home/krolik",
		"krolik 5678  1.0 0.1 20000  20000  ? R+ 00:00 0:00 tar -czf backup.tar.gz /data",
		"krolik 6789  3.0 1.5 300000 300000 ? Sl 00:00 0:10 npm run build",
	}
	for _, line := range cases {
		if got := classifyProcess(line); got != tagBuild {
			t.Errorf("expected %s for %q, got %q", tagBuild, line, got)
		}
	}
}

func TestClassifyProcess_AgentSelf(t *testing.T) {
	cases := []string{
		"krolik 4009318 63.0 0.1 1253892 28864 ? Sl 02:55 0:00 /usr/libexec/docker/cli-plugins/docker-compose compose logs --tail 1000 --since 1h cloakbrowser",
		"krolik 4009363 16.6 0.0 1848384 24536 ? Sl 02:55 0:00 docker stats --no-stream --format {{.Name}}",
		"krolik 9999 10.0 0.5 50000 5000 ? Sl 00:00 0:05 journalctl --user -u dozor --follow",
	}
	for _, line := range cases {
		if got := classifyProcess(line); got != tagAgentSelf {
			t.Errorf("expected %s for %q, got %q", tagAgentSelf, line, got)
		}
	}
}

func TestClassifyProcess_Untagged(t *testing.T) {
	cases := []string{
		"postgres 100 5.0 10.0 500000 50000 ? Sl 00:00 1:00 postgres: checkpointer",
		"redis    200 2.0 5.0 200000 20000 ? Sl 00:00 0:30 redis-server *:6379",
		"root     300 1.0 2.0 100000 10000 ? Sl 00:00 0:10 /usr/lib/systemd/systemd-logind",
		"krolik   400 0.5 1.0 50000 5000 ? S 00:00 0:01 bash",
	}
	for _, line := range cases {
		if got := classifyProcess(line); got != "" {
			t.Errorf("expected empty tag for %q, got %q", line, got)
		}
	}
}

func TestTagTopProcesses_AnnotatesAndCounts(t *testing.T) {
	input := `USER         PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND
krolik   4009318 63.0  0.1 1253892 28864 ?       Sl   02:55   0:00 docker compose logs --tail 1000 cloakbrowser
root     4007753 30.9  0.6 1418096 164184 ?      Rl   02:55   0:03 /usr/local/go/pkg/tool/linux_arm64/compile -o /tmp/go-build1/b009/_pkg_.a
root     1600571 18.9  5.1 2531480 1273096 ?     Ssl  Apr03 1985:09 embed-server
krolik   3188799 13.9  2.5 75420136 627292 pts/4 Rl+  01:18 18:32 claude --dangerously-skip-permissions
postgres 2094147  1.0  0.5 500000 50000 ?        Sl   00:00 1:00 postgres: checkpointer`

	tagged, total, count := tagTopProcesses(input)
	if total != 5 {
		t.Errorf("expected total=5 data rows, got %d", total)
	}
	if count != 3 {
		t.Errorf("expected count=3 tagged rows, got %d", count)
	}
	if !strings.Contains(tagged, tagAgentSelf) {
		t.Error("expected [agent-self] in output")
	}
	if !strings.Contains(tagged, tagBuild) {
		t.Error("expected [build] in output")
	}
	if !strings.Contains(tagged, tagUserSession) {
		t.Error("expected [user-session] in output")
	}
	// postgres line must NOT get any tag.
	for _, line := range strings.Split(tagged, "\n") {
		if strings.Contains(line, "postgres:") && strings.Contains(line, "[") {
			t.Errorf("postgres line should not be tagged: %q", line)
		}
	}
}

func TestTagTopProcesses_Empty(t *testing.T) {
	tagged, total, count := tagTopProcesses("")
	if tagged != "" || total != 0 || count != 0 {
		t.Errorf("expected zero values for empty input, got %q / %d / %d", tagged, total, count)
	}
}

func TestTopProcessLoadBanner_OverHalfTagged(t *testing.T) {
	banner := topProcessLoadBanner(5, 4)
	if banner == "" {
		t.Fatal("expected banner for 4/5 tagged")
	}
	if !strings.Contains(banner, "LOAD SOURCE") {
		t.Errorf("banner should contain LOAD SOURCE, got %q", banner)
	}
	if !strings.Contains(banner, "Do NOT kill") {
		t.Errorf("banner should warn against killing tagged processes, got %q", banner)
	}
}

func TestTopProcessLoadBanner_UnderHalfTagged(t *testing.T) {
	if banner := topProcessLoadBanner(5, 2); banner != "" {
		t.Errorf("expected no banner for 2/5 tagged, got %q", banner)
	}
}

func TestTopProcessLoadBanner_ExactlyHalf(t *testing.T) {
	// Threshold is STRICTLY more than half; 2/4 must not trigger.
	if banner := topProcessLoadBanner(4, 2); banner != "" {
		t.Errorf("expected no banner for exactly half tagged, got %q", banner)
	}
}

func TestTopProcessLoadBanner_Zero(t *testing.T) {
	if banner := topProcessLoadBanner(0, 0); banner != "" {
		t.Errorf("expected no banner for zero input, got %q", banner)
	}
	if banner := topProcessLoadBanner(5, 0); banner != "" {
		t.Errorf("expected no banner when nothing tagged, got %q", banner)
	}
}
