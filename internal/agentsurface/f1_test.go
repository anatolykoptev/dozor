package agentsurface_test

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Fitness function F1: no internal import, no process spawn. The agent tool
// surface and the worker have no call path into code that executes with
// dozor's own privileges, and the surface cannot start a process itself. It is checked on
// the real build graph (go list -deps), for both target OSes, so a transitive
// import added anywhere in the closure fails here.

const (
	modPath = "github.com/anatolykoptev/dozor"
	sdkPkg  = "github.com/modelcontextprotocol/go-sdk/mcp"
)

type listed struct {
	ImportPath string
	Standard   bool
	Imports    []string
	Module     *struct{ Path string }
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		t.Fatal("not inside a module")
	}
	return strings.TrimSuffix(mod, "/go.mod")
}

// closure returns the dependency closure of pattern for goos.
func closure(t *testing.T, goos, pattern string) map[string]listed {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-json", pattern)
	cmd.Dir = moduleRoot(t)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOWORK=off", "CGO_ENABLED=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s (GOOS=%s): %v\n%s", pattern, goos, err, stderr.String())
	}
	pkgs := map[string]listed{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listed
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		pkgs[p.ImportPath] = p
	}
	if _, ok := pkgs[pattern]; !ok && !strings.HasPrefix(pattern, "./") {
		t.Fatalf("go list did not return %s", pattern)
	}
	return pkgs
}

func isOwn(p listed) bool {
	return p.ImportPath == modPath || strings.HasPrefix(p.ImportPath, modPath+"/")
}

func ownPackages(pkgs map[string]listed) []string {
	var own []string
	for _, p := range pkgs {
		if isOwn(p) {
			own = append(own, p.ImportPath)
		}
	}
	slices.Sort(own)
	return own
}

func thirdPartyModules(pkgs map[string]listed) map[string]bool {
	mods := map[string]bool{}
	for _, p := range pkgs {
		if !p.Standard && !isOwn(p) && p.Module != nil {
			mods[p.Module.Path] = true
		}
	}
	return mods
}

var targets = []string{"linux", "darwin"}

// stdAllow is the standard-library surface the agent tool code may import
// directly. os/exec is deliberately absent: go-sdk (a dependency, not our
// code) imports it for its stdio client transport, which is why the check is
// on the direct imports of our own packages rather than on the whole closure.
var stdAllow = []string{
	"bytes", "context", "encoding/base64", "encoding/json", "errors", "fmt", "io", "log/slog",
	"net", "os", "path/filepath", "regexp", "strings", "sync", "syscall", "time", "unicode",
}

// platformAllow is imported by agentproto's darwin build only.
var platformAllow = []string{"golang.org/x/sys/unix"}

func TestF1_NoInternalImport_AgentSurface(t *testing.T) {
	sdk := thirdPartyModules(closure(t, "linux", sdkPkg))
	sdk["golang.org/x/sys"] = true // agentproto's darwin peer-credential call
	for _, goos := range targets {
		pkgs := closure(t, goos, modPath+"/internal/agentsurface")

		wantOwn := []string{modPath + "/internal/agentproto", modPath + "/internal/agentsurface"}
		if got := ownPackages(pkgs); !slices.Equal(got, wantOwn) {
			t.Errorf("GOOS=%s: in-module closure = %v, want %v", goos, got, wantOwn)
		}
		for mod := range thirdPartyModules(pkgs) {
			if !sdk[mod] {
				t.Errorf("GOOS=%s: third-party module %s is outside go-sdk's closure", goos, mod)
			}
		}
		if _, ok := pkgs[sdkPkg]; !ok {
			t.Errorf("GOOS=%s: closure no longer contains go-sdk; the allow-list derivation is stale", goos)
		}
		for _, path := range wantOwn {
			for _, imp := range pkgs[path].Imports {
				if imp == "C" || strings.HasPrefix(imp, modPath+"/") || imp == sdkPkg ||
					slices.Contains(stdAllow, imp) || slices.Contains(platformAllow, imp) {
					continue
				}
				t.Errorf("GOOS=%s: %s imports %q, which is not on the allow-list", goos, path, imp)
			}
		}
	}
}

// workerModules is every third-party module the worker may depend on: x/sys,
// and go-landlock (with its psx helper) for the TCP bind restriction.
var workerModules = []string{
	"golang.org/x/sys",
	"github.com/landlock-lsm/go-landlock",
	"kernel.org/pub/linux/libs/security/libcap/psx",
}

func TestF1_NoInternalImport_Worker(t *testing.T) {
	for _, goos := range targets {
		pkgs := closure(t, goos, modPath+"/cmd/dozor-worker")
		wantOwn := []string{modPath + "/cmd/dozor-worker", modPath + "/internal/agentproto", modPath + "/internal/agentworker"}
		if got := ownPackages(pkgs); !slices.Equal(got, wantOwn) {
			t.Errorf("GOOS=%s: in-module closure = %v, want %v", goos, got, wantOwn)
		}
		for _, forbidden := range []string{
			modPath + "/internal/identity", modPath + "/internal/engine", modPath + "/internal/agentsurface", sdkPkg,
		} {
			if _, ok := pkgs[forbidden]; ok {
				t.Errorf("GOOS=%s: worker closure contains %s", goos, forbidden)
			}
		}
		for mod := range thirdPartyModules(pkgs) {
			if !slices.Contains(workerModules, mod) {
				t.Errorf("GOOS=%s: worker depends on third-party module %s", goos, mod)
			}
		}
	}
}

// TestF1_NoLoopReachesAgentSurface: the Telegram, A2A and claudecode loops
// connect to dozor's operator endpoint themselves; none may reach the agent
// surface, the worker or the identity layer through imports.
func TestF1_NoLoopReachesAgentSurface(t *testing.T) {
	roots := []string{
		modPath + "/internal/toolreg",
		modPath + "/internal/a2a",
		modPath + "/pkg/extensions/claudecode",
	}
	for _, root := range roots {
		for _, goos := range targets {
			pkgs := closure(t, goos, root)
			for _, banned := range []string{
				modPath + "/internal/agentsurface", modPath + "/internal/agentworker", modPath + "/internal/identity",
			} {
				if _, ok := pkgs[banned]; ok {
					t.Errorf("GOOS=%s: %s depends on %s", goos, root, banned)
				}
			}
		}
	}
}

// spawnDenied lists selectors that start a process, by import path. Our own
// non-test code may use none of them: the import allow-list already excludes
// os/exec, and this closes the remaining routes (os.StartProcess,
// syscall exec calls, go-sdk's command transport).
var spawnDenied = map[string][]string{
	"os":      {"StartProcess"},
	"syscall": {"Exec", "ForkExec", "StartProcess"},
	"os/exec": {"*"},
	"github.com/modelcontextprotocol/go-sdk/mcp": {"CommandTransport", "NewCommandTransport"},
}

// TestF1_NoProcessSpawn parses every non-test file of the agent surface and
// the protocol package, for all build tags, and fails on any denied selector.
func TestF1_NoProcessSpawn(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	checked := 0
	for _, dir := range []string{"internal/agentsurface", "internal/agentproto"} {
		files, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no files in %s: %v", dir, err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			checked++
			names := map[string]string{} // local name -> import path
			for _, imp := range f.Imports {
				ip, _ := strconv.Unquote(imp.Path.Value)
				local := filepath.Base(ip)
				if imp.Name != nil {
					local = imp.Name.Name
				}
				if _, bad := spawnDenied[ip]; bad && (local == "_" || local == ".") {
					t.Errorf("%s: %s imported as %q hides its uses", path, ip, local)
				}
				names[local] = ip
			}
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				for _, name := range spawnDenied[names[id.Name]] {
					if name == "*" || name == sel.Sel.Name {
						t.Errorf("%s: %s.%s spawns or enables spawning a process", fset.Position(sel.Pos()), id.Name, sel.Sel.Name)
					}
				}
				return true
			})
		}
	}
	if checked < 5 {
		t.Fatalf("only %d files parsed; the glob is wrong", checked)
	}
}
