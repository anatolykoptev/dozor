package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/anatolykoptev/dozor/internal/identity"
	"github.com/anatolykoptev/go-mcpserver/keyfile"
	"gopkg.in/yaml.v3"
)

// maxKeyDays is the longest a key may live. keyfile requires an expiry but
// cannot cap it (its validator sees only attrs), so mint and lint do.
const (
	maxKeyDays  = 90
	keyFileMode = 0o600
)

var keyNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

const mcpKeyUsage = `dozor mcp-key - manage identity (key-file) MCP keys

Usage:
  dozor mcp-key mint [--prefix dzk] [--name N] [--run-as U] [--days 90] [--tools a,b] [--entry-file F]
      Print a new token ONCE on stdout (redirect it into a 0600 file; a terminal
      is refused) and the key-file entry, which holds only the token's hash, on
      stderr or in --entry-file.
  dozor mcp-key lint <file>
      Validate a key file exactly as the server loads it and print the effective
      tool set per key. Exit 1 on any problem.
`

// runMCPKey is the "dozor mcp-key" entry point; it returns the exit code.
// It needs no environment, engine or network.
func runMCPKey(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, mcpKeyUsage)
		return 2
	}
	switch args[0] {
	case "mint":
		return mintKey(args[1:], stdout, stderr)
	case "lint":
		return lintKeyFile(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprint(stderr, mcpKeyUsage)
		return 2
	}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

type mintOpts struct {
	prefix, name, runAs, entryFile string
	days                           int
	tools                          []string
}

// parseMint reads and validates the mint flags. ok is false when the caller
// should exit with code.
func parseMint(args []string, stderr io.Writer) (o mintOpts, code int, ok bool) {
	fs := flag.NewFlagSet("mcp-key mint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	prefix := fs.String("prefix", identity.TokenPrefix, "token prefix, [a-z]{2,8}")
	name := fs.String("name", "", "key name (entry placeholder if empty)")
	runAs := fs.String("run-as", "", "agent identity the key runs as (default: the name)")
	days := fs.Int("days", maxKeyDays, fmt.Sprintf("lifetime in days, 1..%d", maxKeyDays))
	toolsFlag := fs.String("tools", strings.Join(agentsurface.ToolNames, ","), "comma-separated tools to grant")
	entryFile := fs.String("entry-file", "", "write the key-file entry here (created 0600, must not exist) instead of stderr")
	if err := fs.Parse(args); err != nil {
		return o, 2, false
	}
	o = mintOpts{prefix: *prefix, name: *name, runAs: *runAs, entryFile: *entryFile, days: *days, tools: splitList(*toolsFlag)}
	if o.runAs == "" {
		o.runAs = o.name
	}
	switch {
	case o.days < 1 || o.days > maxKeyDays:
		_, _ = fmt.Fprintf(stderr, "mcp-key: --days must be 1..%d\n", maxKeyDays)
	case o.name != "" && (!keyNameRE.MatchString(o.name) || identity.Reserved(o.name)):
		_, _ = fmt.Fprintln(stderr, "mcp-key: --name must match ^[a-z][a-z0-9-]{1,31}$ and not use a reserved prefix")
	default:
		for _, t := range o.tools {
			if !slices.Contains(agentsurface.ToolNames, t) {
				_, _ = fmt.Fprintf(stderr, "mcp-key: %q is not an agent tool (%s)\n", t, strings.Join(agentsurface.ToolNames, ", "))
				return o, 2, false
			}
		}
		return o, 0, true
	}
	return o, 2, false
}

// writeEntry puts the key-file entry (hash only) in path, created 0600 and
// never overwritten, or on stderr when path is empty.
func writeEntry(path, entry string, stderr io.Writer) error {
	if path == "" {
		_, err := io.WriteString(stderr, entry)
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, keyFileMode)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, entry); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func mintKey(args []string, stdout, stderr io.Writer) int {
	o, code, ok := parseMint(args, stderr)
	if !ok {
		return code
	}
	// The token goes to stdout only, and never to a terminal: scrollback and
	// multiplexer logs are not secret stores.
	if isTerminal(stdout) {
		_, _ = fmt.Fprintln(stderr, "mcp-key: refusing to print a token to a terminal; redirect stdout into a 0600 file:\n"+
			"  umask 077; dozor mcp-key mint ... > token.out")
		return 1
	}
	token, hash, err := keyfile.Mint(o.prefix)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp-key: %v\n", err)
		return 1
	}
	if o.prefix != identity.TokenPrefix {
		_, _ = fmt.Fprintf(stderr, "mcp-key: warning: dozor accepts only the prefix %q; this token will be rejected\n", identity.TokenPrefix)
	}
	entry := renderEntry(o.name, o.runAs, hash, time.Now().UTC().Add(time.Duration(o.days)*24*time.Hour), o.tools)
	if err := writeEntry(o.entryFile, entry, stderr); err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp-key: writing the entry: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, token); err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp-key: writing the token: %v\n", err)
		return 1
	}
	return 0
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// renderEntry is one key-file entry. Placeholders for a missing name and
// run_as fail lint on purpose until the operator fills them in.
func renderEntry(name, runAs, hash string, expires time.Time, tools []string) string {
	if name == "" {
		name = "REPLACE-ME"
	}
	if runAs == "" {
		runAs = "REPLACE-ME"
	}
	return fmt.Sprintf("  - name: %s\n    sha256: %s\n    expires: %s\n    tools: [%s]\n    attrs: {%s: %s, %s: %s}\n",
		name, hash, expires.Format(time.RFC3339), strings.Join(tools, ", "),
		identity.AttrRole, identity.RoleAgentUser, identity.AttrRunAs, runAs)
}

// logCapture records what keyfile logs while loading, so lint can report why
// an entry was dropped. keyfile never logs token material.
type logCapture struct {
	mu   *sync.Mutex
	recs *[]slog.Record
}

func (c logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.recs = append(*c.recs, r.Clone())
	return nil
}
func (c logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c logCapture) WithGroup(string) slog.Handler      { return c }

type lintFile struct {
	Keys []struct {
		Name    string            `yaml:"name"`
		Expires string            `yaml:"expires"`
		Tools   []string          `yaml:"tools"`
		Attrs   map[string]string `yaml:"attrs"`
	} `yaml:"keys"`
}

// loadProblems reports every ERROR keyfile logged while loading, and the names
// of the entries it dropped.
func loadProblems(recs []slog.Record, problem func(string, ...any)) map[string]bool {
	dropped := map[string]bool{}
	for _, r := range recs {
		if r.Level < slog.LevelError {
			continue
		}
		var parts []string
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "name" {
				dropped[a.Value.String()] = true
			}
			parts = append(parts, a.Key+"="+a.Value.String())
			return true
		})
		problem("%s %s", r.Message, strings.Join(parts, " "))
	}
	return dropped
}

func lintKeyFile(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprint(stderr, mcpKeyUsage)
		return 2
	}
	path := args[0]
	raw, err := os.ReadFile(path) //nolint:gosec // the operator names the file to check
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp-key lint: %v\n", err)
		return 1
	}

	var (
		mu   sync.Mutex
		recs []slog.Record
	)
	// The same Options the server uses: what loads here loads there.
	kf, err := keyfile.Open(identity.KeyfileOptions(path, slog.New(logCapture{mu: &mu, recs: &recs})))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mcp-key lint: %v\n", err)
		return 1
	}
	problems := 0
	problem := func(format string, a ...any) {
		problems++
		_, _ = fmt.Fprintf(stderr, "PROBLEM: "+format+"\n", a...)
	}

	dropped := loadProblems(recs, problem)
	if !kf.Status().LastReloadOK {
		problem("the key file is invalid; the server would deny every key")
		return 1
	}

	var f lintFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		problem("cannot read the entries back: %v", err)
		return 1
	}
	limit := time.Now().UTC().Add(maxKeyDays * 24 * time.Hour)
	for _, k := range f.Keys {
		if dropped[k.Name] {
			continue
		}
		if exp, err := time.Parse(time.RFC3339, k.Expires); err == nil && exp.After(limit) {
			problem("key %q expires %s, more than %d days from now", k.Name, k.Expires, maxKeyDays)
		}
		_, _ = fmt.Fprintf(stdout, "%s\trole=%s\trun_as=%s\texpires=%s\ttools=%s\n", k.Name,
			k.Attrs[identity.AttrRole], k.Attrs[identity.AttrRunAs], k.Expires,
			strings.Join(identity.EffectiveTools(k.Attrs[identity.AttrRole], k.Tools), ","))
	}
	if kf.Status().Keys != len(f.Keys) && problems == 0 {
		problem("%d of %d entries were dropped", len(f.Keys)-kf.Status().Keys, len(f.Keys))
	}
	if problems > 0 {
		return 1
	}
	return 0
}
