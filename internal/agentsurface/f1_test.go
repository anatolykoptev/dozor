package agentsurface_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// Fitness function F1: the agent tool surface and the worker have no call
// path into code that executes with dozor's own privileges. It is checked on
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

func TestF1_AgentSurfaceClosure(t *testing.T) {
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

func TestF1_WorkerClosure(t *testing.T) {
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
			if mod != "golang.org/x/sys" {
				t.Errorf("GOOS=%s: worker depends on third-party module %s", goos, mod)
			}
		}
	}
}

// TestF1_NoLoopReachesAgentSurface: the Telegram, A2A and claudecode loops
// connect to dozor's operator endpoint themselves; none may reach the agent
// surface or the worker through imports.
func TestF1_NoLoopReachesAgentSurface(t *testing.T) {
	roots := []string{
		modPath + "/internal/toolreg",
		modPath + "/internal/a2a",
		modPath + "/pkg/extensions/claudecode",
	}
	for _, root := range roots {
		for _, goos := range targets {
			pkgs := closure(t, goos, root)
			for _, banned := range []string{modPath + "/internal/agentsurface", modPath + "/internal/agentworker"} {
				if _, ok := pkgs[banned]; ok {
					t.Errorf("GOOS=%s: %s depends on %s", goos, root, banned)
				}
			}
		}
	}
}
