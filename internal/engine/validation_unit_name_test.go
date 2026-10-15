package engine

import (
	"strings"
	"testing"
)

func TestValidateUnitName(t *testing.T) {
	valid := []string{"dozor", "go-wp", "user@1000.service", "nginx.service", "my_container.1", "a", "9lives", "getty@tty1.service", "x:y"}
	for _, n := range valid {
		if ok, why := ValidateUnitName(n); !ok {
			t.Errorf("ValidateUnitName(%q) rejected a valid name: %s", n, why)
		}
	}
	invalid := []string{"", "-h", "--help", "-x", "x; id", "$(id)", "`id`", "a|b", "a&&b", "a\nb", "a b", `a"b`, "a'b", "../x", "a/b", ".hidden", "_x", "@x", strings.Repeat("a", 256)}
	for _, n := range invalid {
		if ok, _ := ValidateUnitName(n); ok {
			t.Errorf("ValidateUnitName(%q) accepted an invalid name", n)
		}
	}
	if ok, _ := ValidateUnitName(strings.Repeat("a", 255)); !ok {
		t.Error("255-char name must be accepted")
	}
}

func TestValidateUnitName_LeadingDashRejected(t *testing.T) {
	if ok, _ := ValidateUnitName("-h"); ok {
		t.Error("a leading '-' must be rejected: it would be parsed as a command-line option")
	}
}

func TestValidateRepoPath(t *testing.T) {
	for _, p := range []string{"/home/user/src/dozor", "/srv/app", "/a/b_c/d-e/f.g"} {
		if ok, why := ValidateRepoPath(p); !ok {
			t.Errorf("ValidateRepoPath(%q) rejected: %s", p, why)
		}
	}
	for _, p := range []string{"", "-h", "relative", ".", "..", "/a/../b", "/a//b", "/a/b/", "/a b", "/a;b", "/a$b", "/a`b", "/a\nb", "/-x/../y"} {
		if ok, _ := ValidateRepoPath(p); ok {
			t.Errorf("ValidateRepoPath(%q) accepted", p)
		}
	}
}

func TestValidateNucleiSeverities(t *testing.T) {
	for _, s := range []string{"", "high", "critical,high,medium", "info,low,unknown"} {
		if ok, why := ValidateNucleiSeverities(s); !ok {
			t.Errorf("rejected %q: %s", s, why)
		}
	}
	for _, s := range []string{"HIGH", "high,", ",high", "high low", "bogus", "high;id"} {
		if ok, _ := ValidateNucleiSeverities(s); ok {
			t.Errorf("accepted %q", s)
		}
	}
}
