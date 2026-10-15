package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var tokenRE = regexp.MustCompile(`^dzk1_[0-9A-Za-z]{49}$`)

// mint runs "mcp-key mint" with stdout redirected into a regular file (the
// way the operator runs it) and returns the token, the stderr text and the code.
func mint(t *testing.T, args ...string) (token, stderr string, code int) {
	t.Helper()
	out, err := os.Create(filepath.Join(t.TempDir(), "token.out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var errb bytes.Buffer
	code = runMCPKey(append([]string{"mint"}, args...), out, &errb)
	b, _ := os.ReadFile(out.Name())
	return strings.TrimSpace(string(b)), errb.String(), code
}

func TestMint_TokenFormatAndHashEntry(t *testing.T) {
	token, stderr, code := mint(t, "--name", "muse", "--run-as", "muse")
	if code != 0 || !tokenRE.MatchString(token) {
		t.Fatalf("code %d token %q stderr %s", code, token, stderr)
	}
	sum := sha256.Sum256([]byte(token))
	if !strings.Contains(stderr, hex.EncodeToString(sum[:])) {
		t.Errorf("the entry lacks the token's sha256:\n%s", stderr)
	}
	if strings.Contains(stderr, token) || strings.Contains(stderr, token[5:20]) {
		t.Errorf("the token (or a part of it) leaked to stderr:\n%s", stderr)
	}
	for _, want := range []string{"name: muse", "role: agent-user", "run_as: muse", "agent_exec"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("entry lacks %q:\n%s", want, stderr)
		}
	}
}

func TestMint_RefusesTerminal(t *testing.T) {
	// /dev/null is a character device, like a tty.
	tty, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()
	var errb bytes.Buffer
	if code := runMCPKey([]string{"mint"}, tty, &errb); code == 0 || !strings.Contains(errb.String(), "terminal") {
		t.Errorf("code %d stderr %q, want a refusal", code, errb.String())
	}
}

func TestMint_EntryFileIs0600AndNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entry.yaml")
	token, _, code := mint(t, "--name", "muse", "--entry-file", path)
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("entry file: %v mode %v", err, fi)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), token) {
		t.Error("token in the entry file")
	}
	if _, _, code := mint(t, "--name", "muse", "--entry-file", path); code == 0 {
		t.Error("an existing entry file was overwritten")
	}
}

func TestMint_RejectsBadInput(t *testing.T) {
	for name, args := range map[string][]string{
		"days over the ceiling": {"--days", "91"},
		"zero days":             {"--days", "0"},
		"reserved name":         {"--name", "operator"},
		"env- name":             {"--name", "env-prod"},
		"bad name":              {"--name", "Muse!"},
		"operator tool":         {"--tools", "server_exec"},
		"bad prefix":            {"--prefix", "X"},
	} {
		if _, _, code := mint(t, args...); code == 0 {
			t.Errorf("%s accepted", name)
		}
	}
}

func writeKeyFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp-keys.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nkeys:\n"+body+"end: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func lint(t *testing.T, path string) (out, errs string, code int) {
	t.Helper()
	var o, e bytes.Buffer
	code = runMCPKey([]string{"lint", path}, &o, &e)
	return o.String(), e.String(), code
}

func entryWith(t *testing.T, name, attrs string, exp time.Time, tools string) string {
	t.Helper()
	k := newTestKey(t, name, "", "")
	return fmt.Sprintf("  - name: %s\n    sha256: %s\n    expires: %s\n    tools: [%s]\n    attrs: {%s}\n",
		name, k.hash, exp.UTC().Format(time.RFC3339), tools, attrs)
}

// lint validates against the same Options as the server and reports each
// way an entry is wrong; a good entry prints its effective tool set.
func TestLint(t *testing.T) {
	soon := time.Now().Add(30 * 24 * time.Hour)
	good := "role: agent-user, run_as: muse"
	cases := map[string]struct {
		body    string
		wantOK  bool
		wantOut string // in stdout when ok, in stderr otherwise
	}{
		"good": {entryWith(t, "muse", good, soon, "agent_exec, agent_status"), true, "tools=agent_exec,agent_status"},
		"operator role": {entryWith(t, "muse", "role: operator, run_as: muse", soon, "agent_exec"), false,
			"role operator is not allowed"},
		"run_as root":  {entryWith(t, "muse", "role: agent-user, run_as: root", soon, "agent_exec"), false, "not a valid agent identity"},
		"unknown attr": {entryWith(t, "muse", good+", exec: full", soon, "agent_exec"), false, "unknown attr"},
		"no role":      {entryWith(t, "muse", "run_as: muse", soon, "agent_exec"), false, "role is required"},
		"operator tool": {entryWith(t, "muse", good, soon, "agent_exec, server_exec"), false,
			"entry dropped"},
		"reserved name": {entryWith(t, "operator-x", good, soon, "agent_exec"), false, "reserved name"},
		"over 90 days":  {entryWith(t, "muse", good, time.Now().Add(200*24*time.Hour), "agent_exec"), false, "more than 90 days"},
		"expired":       {entryWith(t, "muse", good, time.Now().Add(-time.Hour), "agent_exec"), false, "expired"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out, errs, code := lint(t, writeKeyFile(t, c.body))
			if (code == 0) != c.wantOK {
				t.Fatalf("code %d, want ok=%v\nstdout: %s\nstderr: %s", code, c.wantOK, out, errs)
			}
			text := errs
			if c.wantOK {
				text = out
			}
			if !strings.Contains(text, c.wantOut) {
				t.Errorf("missing %q\nstdout: %s\nstderr: %s", c.wantOut, out, errs)
			}
		})
	}
}

func TestLint_FileProblems(t *testing.T) {
	if _, _, code := lint(t, filepath.Join(t.TempDir(), "absent.yaml")); code == 0 {
		t.Error("missing file passed lint")
	}
	bad := filepath.Join(t.TempDir(), "k.yaml")
	if err := os.WriteFile(bad, []byte("version: 1\nkeys: []\n"), 0o600); err != nil { // no end marker
		t.Fatal(err)
	}
	if _, errs, code := lint(t, bad); code == 0 {
		t.Errorf("file without the end marker passed lint: %s", errs)
	}
	if code := runMCPKey([]string{"lint"}, new(bytes.Buffer), new(bytes.Buffer)); code != 2 {
		t.Errorf("lint without a file: %d, want 2", code)
	}
	if code := runMCPKey(nil, new(bytes.Buffer), new(bytes.Buffer)); code != 2 {
		t.Errorf("no subcommand: %d, want 2", code)
	}
}

// What mint prints is what lint accepts, once the name is filled in.
func TestMintOutputPassesLint(t *testing.T) {
	_, entry, code := mint(t, "--name", "muse", "--run-as", "muse")
	if code != 0 {
		t.Fatal(entry)
	}
	if out, errs, code := lint(t, writeKeyFile(t, entry)); code != 0 || !strings.Contains(out, "run_as="+"muse") {
		t.Errorf("code %d\n%s\n%s", code, out, errs)
	}
	// Without --name the placeholders fail lint on purpose.
	_, placeholder, _ := mint(t)
	if _, _, code := lint(t, writeKeyFile(t, placeholder)); code == 0 {
		t.Error("a placeholder entry passed lint")
	}
}
