// Package fakessh installs a recording stand-in for the ssh binary so tests
// can observe every command a remote-mode engine.Transport would run, without
// running any of them.
package fakessh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Hostile names that must never reach a shell as part of a unit, container
// or service name.
var HostileNames = []string{
	"x; id", "$(id)", "`id`", "a|b", "a&&b", "a\nb", "-h", "--help",
	"a b", `a"b`, "a'b", "../x", "x/../y", "a>b", "a<b", "a#b", "a*b",
	"dozor\n", "a\x00b", "é", "a\u200bb",
}

// HostilePaths are hostile or malformed repository paths.
var HostilePaths = []string{
	"/tmp/x; id", "/tmp/$(id)", "/tmp/`id`", "/tmp/a|b", "/tmp/a&&b", "/tmp/a\nb",
	"-h", "--help", "/tmp/a b", `/tmp/a"b`, "/tmp/a'b", "../x", "/tmp/../etc", "relative/path", "/tmp//x", "/tmp/x/",
	"~/x; id", "~/../x", "~x", "~", "/tmp/dozor\n", "/tmp/é", "/tmp/a\u200bb",
}

// Install puts a recording fake "ssh" first on PATH and returns a function
// that reports every command (the last ssh argument) recorded so far.
func Install(t *testing.T) (commands func() []string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "commands.log")
	script := "#!/bin/bash\nprintf '%s\\0' \"${@: -1}\" >> \"" + logPath + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil { //nolint:gosec // test stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	}
}

// Contains reports the first recorded command containing needle, if any.
func Contains(cmds []string, needle string) (string, bool) {
	for _, c := range cmds {
		if strings.Contains(c, needle) {
			return c, true
		}
	}
	return "", false
}
