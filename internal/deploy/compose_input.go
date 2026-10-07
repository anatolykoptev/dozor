package deploy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// composeEntryCandidates is docker compose's default file-discovery order
// (compose v2): the first existing file is the project entry point.
var composeEntryCandidates = []string{
	"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml",
}

// composeOverrideCandidates are auto-loaded on top of the entry file whenever
// present — including when untracked — so each existing one is compose input
// for every service in the project.
var composeOverrideCandidates = []string{
	"compose.override.yaml", "compose.override.yml",
	"docker-compose.override.yaml", "docker-compose.override.yml",
}

// staleComposeInputs reports which compose-input files for `services` differ
// between the deploy clone's working tree and origin/<branch>. The clone-
// relative input set is derived from the compose project rooted at
// composePath: the entry file, auto-loaded override files, the include chain,
// and each deployed service's env_file / extends.file references. Two change
// sets are intersected with that input:
//
//   - `git diff --name-only origin/<branch>` — every tracked file whose
//     working-tree content differs from origin, covering both local dirty
//     edits AND a HEAD left behind by a skipped pull (issue #239);
//   - `??` porcelain entries — untracked files that compose still reads
//     (auto-loaded overrides, glob-include matches); git diff cannot see them.
//
// `.env` is deliberately NOT input: it is gitignored and exists only in the
// live clone, so it can never diverge from origin — counting it would block
// every deploy.
//
// An error means freshness could not be established — the caller must fail
// closed: "cannot verify" is not "verified fresh".
func staleComposeInputs(ctx context.Context, clonePath, composePath, branch string, services []string) ([]string, error) {
	if clonePath == "" {
		return nil, nil // no deploy clone configured — nothing to verify against
	}
	if composePath == "" {
		return nil, errors.New("compose_path is empty — cannot resolve compose input")
	}
	branch = resolveDeployBranch(ctx, clonePath, branch)
	ref := "origin/" + branch

	idx, err := buildComposeInputSet(composePath, clonePath)
	if err != nil {
		return nil, err
	}
	input := idx.forServices(services)

	// --end-of-options + --no-renames mirror releaseChangedFiles: the ref is
	// config-derived, never an option; a rename lists old and new names as
	// separate entries so a renamed-away input file still intersects.
	diffOut, err := outputRunner(ctx, clonePath,
		"git", "diff", "--name-only", "--no-renames", "--end-of-options", ref) //nolint:gosec // trusted local config, not shell
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only %s: %w", ref, err)
	}
	statusOut, err := gitStatusRunner(ctx, clonePath)
	if err != nil {
		return nil, fmt.Errorf("git status --porcelain: %w", err)
	}

	var stale []string
	seen := make(map[string]struct{})
	collect := func(p string) {
		p = unquoteGitPath(strings.TrimSpace(p))
		if p == "" {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		// A changed file matching a glob-include pattern is potential input
		// for every service — the pattern does not say which services the
		// matched file defines.
		if _, ok := input[p]; ok || MatchPath(p, idx.wilds) {
			seen[p] = struct{}{}
			stale = append(stale, p)
		}
	}
	for _, line := range strings.Split(string(diffOut), "\n") {
		collect(line)
	}
	for _, p := range untrackedPaths(string(statusOut)) {
		collect(p)
	}
	sort.Strings(stale)
	return stale, nil
}

// composeInputIndex maps the compose project rooted at composePath onto
// clone-relative file sets: which files a `docker compose` render of the
// deployed services actually reads.
type composeInputIndex struct {
	global  map[string]struct{}            // entry + override files + include-level env_file — input for every service
	bySvc   map[string]map[string]struct{} // service → defining compose file(s) + env_file + extends.file
	all     map[string]struct{}            // every parsed compose-input file inside the clone
	wilds   []string                       // glob include patterns, clone-relative
	visited map[string]struct{}            // absolute paths already walked (include cycle guard)
}

// forServices returns the clone-relative input set for the deployed services:
// the global files plus each service's attributed files. An unmapped service
// (not defined in any parsed file) or an empty service list falls back to the
// whole parsed input — conservative, since compose itself would fail or would
// render the entire project.
func (s *composeInputIndex) forServices(services []string) map[string]struct{} {
	out := make(map[string]struct{}, len(s.global)+4)
	maps.Copy(out, s.global)
	if len(services) == 0 {
		maps.Copy(out, s.all)
		return out
	}
	for _, svc := range services {
		files, ok := s.bySvc[svc]
		if !ok {
			maps.Copy(out, s.all)
			continue
		}
		maps.Copy(out, files)
	}
	return out
}

// buildComposeInputSet parses the compose project under composePath and
// attributes every compose-input file to the services it affects. File paths
// are recorded clone-relative; inputs outside the clone (absolute env_file
// paths elsewhere on the host) are not git-tracked here and are skipped —
// they can never diverge from origin/<branch>.
func buildComposeInputSet(composePath, clonePath string) (*composeInputIndex, error) {
	idx := &composeInputIndex{
		global:  make(map[string]struct{}),
		bySvc:   make(map[string]map[string]struct{}),
		all:     make(map[string]struct{}),
		visited: make(map[string]struct{}),
	}

	var entry string
	for _, name := range composeEntryCandidates {
		p := filepath.Join(composePath, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			entry = p
			break
		}
	}
	if entry == "" {
		return nil, fmt.Errorf("no compose entry file in %s (tried %s)",
			composePath, strings.Join(composeEntryCandidates, ", "))
	}
	if rel, ok := repoRel(clonePath, entry); ok {
		idx.global[rel] = struct{}{}
	}
	if err := idx.walk(entry, clonePath); err != nil {
		return nil, err
	}

	for _, name := range composeOverrideCandidates {
		p := filepath.Join(composePath, name)
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			continue
		}
		if rel, ok := repoRel(clonePath, p); ok {
			idx.global[rel] = struct{}{}
		}
		if err := idx.walk(p, clonePath); err != nil {
			return nil, err
		}
	}
	return idx, nil
}

// composeDoc is the subset of a compose file the input index needs.
type composeDoc struct {
	Include  yaml.Node              `yaml:"include"`
	Services map[string]serviceNode `yaml:"services"`
}

type serviceNode struct {
	EnvFile yaml.Node `yaml:"env_file"`
	Extends struct {
		File string `yaml:"file"`
	} `yaml:"extends"`
}

// walk parses one compose file and records it: the file itself into `all`,
// each service it defines into bySvc, env_file/extends references of those
// services likewise, and recursively walks `include` items. Absolute paths
// are visited-once so include cycles terminate.
func (s *composeInputIndex) walk(absPath, clonePath string) error {
	absPath = filepath.Clean(absPath)
	if _, ok := s.visited[absPath]; ok {
		return nil
	}
	s.visited[absPath] = struct{}{}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("compose input %s: %w", absPath, err)
	}
	if rel, ok := repoRel(clonePath, absPath); ok {
		s.all[rel] = struct{}{}
	}

	var doc composeDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse compose file %s: %w", absPath, err)
	}

	dir := filepath.Dir(absPath)
	selfRel, selfOK := repoRel(clonePath, absPath)
	for svc, svcNode := range doc.Services {
		if selfOK {
			s.addSvc(svc, selfRel)
		}
		for _, p := range nodePaths(&svcNode.EnvFile) {
			if rel, ok := repoRel(clonePath, resolveChild(dir, p)); ok {
				s.addSvc(svc, rel)
				s.all[rel] = struct{}{}
			}
		}
		if f := svcNode.Extends.File; f != "" {
			child := resolveChild(dir, f)
			if rel, ok := repoRel(clonePath, child); ok {
				s.addSvc(svc, rel)
			}
			if err := s.walk(child, clonePath); err != nil {
				return err
			}
		}
	}

	for _, item := range includeNodes(doc.Include) {
		// `include: - {path: x.yml, env_file: e.env}` — the include-level env
		// file feeds the included services; attributed globally (simple and
		// conservative: these files are typically shared anyway).
		for _, p := range nodePaths(mappingValue(item, "env_file")) {
			if rel, ok := repoRel(clonePath, resolveChild(dir, p)); ok {
				s.global[rel] = struct{}{}
			}
		}
		for _, p := range nodePaths(item) {
			child := resolveChild(dir, p)
			if hasGlobChars(p) {
				if rel, ok := repoRel(clonePath, child); ok {
					s.wilds = append(s.wilds, rel)
				}
				matches, gerr := filepath.Glob(child)
				if gerr != nil {
					return fmt.Errorf("compose include glob %s: %w", p, gerr)
				}
				for _, m := range matches {
					if err := s.walk(m, clonePath); err != nil {
						return err
					}
				}
				continue
			}
			if err := s.walk(child, clonePath); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *composeInputIndex) addSvc(svc, rel string) {
	m := s.bySvc[svc]
	if m == nil {
		m = make(map[string]struct{})
		s.bySvc[svc] = m
	}
	m[rel] = struct{}{}
}

// includeNodes normalises the polymorphic `include:` field into element
// nodes: a sequence's items, or a single scalar/mapping as a one-element
// slice.
func includeNodes(n yaml.Node) []*yaml.Node {
	if n.Kind == 0 {
		return nil
	}
	if n.Kind == yaml.SequenceNode {
		return n.Content
	}
	return []*yaml.Node{&n}
}

// mappingValue returns the value node of `key` in a mapping node, or nil.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// nodePaths extracts path strings from a polymorphic YAML node: a scalar, a
// sequence of scalars or {path: ...} mappings, or a single {path: ...}
// mapping. `file` is accepted alongside `path` (extends syntax).
func nodePaths(n *yaml.Node) []string {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Value == "" {
			return nil
		}
		return []string{n.Value}
	case yaml.SequenceNode:
		var out []string
		for _, item := range n.Content {
			out = append(out, nodePaths(item)...)
		}
		return out
	case yaml.MappingNode:
		if v := mappingValue(n, "path"); v != nil {
			return nodePaths(v)
		}
		if v := mappingValue(n, "file"); v != nil {
			return nodePaths(v)
		}
	}
	return nil
}

// resolveChild resolves a compose-relative path (include / env_file /
// extends.file) against the directory of the file referencing it — the same
// base docker compose uses.
func resolveChild(dir, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(dir, p)
}

// repoRel maps an absolute path to a clone-relative path usable against
// `git diff`/`git status` output. Returns false for paths outside the clone —
// they are not tracked there and cannot diverge from origin/<branch>.
func repoRel(root, abs string) (string, bool) {
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func hasGlobChars(p string) bool {
	return strings.ContainsAny(p, "*?[")
}

// untrackedPaths (source_sync.go) returns the file paths of `??` porcelain
// lines — files git diff cannot see but compose may still read (overrides,
// glob includes).

// unquoteGitPath reverses git's C-style quoting of unusual path names
// (core.quotePath); on a malformed quote the raw string is kept.
func unquoteGitPath(p string) string {
	if len(p) >= 2 && strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}
