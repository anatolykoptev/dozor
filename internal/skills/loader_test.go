package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- helpers ---

// makeSkillDir creates <dir>/<skillName>/SKILL.md with the given content.
func makeSkillDir(t *testing.T, dir, skillName, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, skillName)
	if err := os.MkdirAll(skillDir, 0750); err != nil {
		t.Fatalf("mkdir %s: %v", skillDir, err)
	}
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", skillFile, err)
	}
}

// skillMD builds a SKILL.md content string with optional frontmatter.
func skillMD(name, description, body string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body
}

// --- ListSkills tests ---

func TestListSkills_SingleBuiltin(t *testing.T) {
	builtinDir := t.TempDir()
	makeSkillDir(t, builtinDir, "escalation", skillMD("escalation", "Handle escalation", "## Steps\n1. Alert."))

	loader := NewLoader("", builtinDir)
	skills := loader.ListSkills()

	if len(skills) != 1 {
		t.Fatalf("len: got %d, want 1", len(skills))
	}
	si := skills[0]
	if si.Name != "escalation" {
		t.Errorf("Name: got %q, want %q", si.Name, "escalation")
	}
	if si.Description != "Handle escalation" {
		t.Errorf("Description: got %q, want %q", si.Description, "Handle escalation")
	}
	if si.Source != "builtin" {
		t.Errorf("Source: got %q, want %q", si.Source, "builtin")
	}
	if si.Path == "" {
		t.Error("Path is empty, want non-empty")
	}
}

func TestListSkills_MultipleSkills_SortedByName(t *testing.T) {
	builtinDir := t.TempDir()
	makeSkillDir(t, builtinDir, "zebra", skillMD("zebra", "Zebra skill", "body"))
	makeSkillDir(t, builtinDir, "alpha", skillMD("alpha", "Alpha skill", "body"))
	makeSkillDir(t, builtinDir, "middle", skillMD("middle", "Middle skill", "body"))

	loader := NewLoader("", builtinDir)
	skills := loader.ListSkills()

	if len(skills) != 3 {
		t.Fatalf("len: got %d, want 3", len(skills))
	}
	names := []string{skills[0].Name, skills[1].Name, skills[2].Name}
	want := []string{"alpha", "middle", "zebra"}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("skills[%d].Name: got %q, want %q", i, names[i], want[i])
		}
	}
}

func TestListSkills_WorkspaceOverridesBuiltin(t *testing.T) {
	workspaceDir := t.TempDir()
	builtinDir := t.TempDir()

	// Same skill name in both — workspace wins.
	makeSkillDir(t, workspaceDir, "diagnostics", skillMD("diagnostics", "Workspace version", "workspace body"))
	makeSkillDir(t, builtinDir, "diagnostics", skillMD("diagnostics", "Builtin version", "builtin body"))

	loader := NewLoader(workspaceDir, builtinDir)
	skills := loader.ListSkills()

	if len(skills) != 1 {
		t.Fatalf("len: got %d, want 1 (workspace overrides builtin)", len(skills))
	}
	if skills[0].Description != "Workspace version" {
		t.Errorf("Description: got %q, want %q", skills[0].Description, "Workspace version")
	}
	if skills[0].Source != "workspace" {
		t.Errorf("Source: got %q, want %q", skills[0].Source, "workspace")
	}
}

func TestListSkills_WorkspaceAndBuiltinCombined(t *testing.T) {
	workspaceDir := t.TempDir()
	builtinDir := t.TempDir()

	makeSkillDir(t, workspaceDir, "custom", skillMD("custom", "Custom only", "body"))
	makeSkillDir(t, builtinDir, "builtin-only", skillMD("builtin-only", "Builtin only", "body"))

	loader := NewLoader(workspaceDir, builtinDir)
	skills := loader.ListSkills()

	if len(skills) != 2 {
		t.Fatalf("len: got %d, want 2", len(skills))
	}

	byName := make(map[string]SkillInfo)
	for _, s := range skills {
		byName[s.Name] = s
	}

	if byName["custom"].Source != "workspace" {
		t.Errorf("custom.Source: got %q, want workspace", byName["custom"].Source)
	}
	if byName["builtin-only"].Source != "builtin" {
		t.Errorf("builtin-only.Source: got %q, want builtin", byName["builtin-only"].Source)
	}
}

func TestListSkills_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	loader := NewLoader("", dir)
	skills := loader.ListSkills()
	if len(skills) != 0 {
		t.Errorf("expected 0 skills from empty dir, got %d", len(skills))
	}
}

func TestListSkills_BothDirsEmpty(t *testing.T) {
	workspace := t.TempDir()
	builtin := t.TempDir()
	loader := NewLoader(workspace, builtin)
	skills := loader.ListSkills()
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestListSkills_NoDirs(t *testing.T) {
	loader := NewLoader("", "")
	skills := loader.ListSkills()
	if len(skills) != 0 {
		t.Errorf("expected 0 skills with empty dirs, got %d", len(skills))
	}
}

func TestListSkills_NonExistentDir(t *testing.T) {
	loader := NewLoader("", "/tmp/does-not-exist-dozor-test-12345")
	skills := loader.ListSkills()
	if len(skills) != 0 {
		t.Errorf("expected 0 skills for nonexistent dir, got %d", len(skills))
	}
}

func TestListSkills_SkipsFilesAtTopLevel(t *testing.T) {
	dir := t.TempDir()
	// A file (not directory) at top level should be skipped.
	if err := os.WriteFile(filepath.Join(dir, "notadir.md"), []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	// A directory without SKILL.md should be skipped.
	if err := os.MkdirAll(filepath.Join(dir, "nodoc"), 0750); err != nil {
		t.Fatal(err)
	}

	loader := NewLoader("", dir)
	skills := loader.ListSkills()
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestListSkills_FallsBackToDirNameWhenNoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	// SKILL.md with no frontmatter — name falls back to directory name.
	makeSkillDir(t, dir, "my-skill", "Just plain content, no frontmatter.")

	loader := NewLoader("", dir)
	skills := loader.ListSkills()

	if len(skills) != 1 {
		t.Fatalf("len: got %d, want 1", len(skills))
	}
	if skills[0].Name != "my-skill" {
		t.Errorf("Name: got %q, want %q", skills[0].Name, "my-skill")
	}
}

// --- LoadSkill tests ---

func TestLoadSkill_Found(t *testing.T) {
	dir := t.TempDir()
	body := "## Instructions\nDo the thing."
	makeSkillDir(t, dir, "diagnostics", skillMD("diagnostics", "Diagnose issues", body))

	loader := NewLoader("", dir)
	content, ok := loader.LoadSkill("diagnostics")
	if !ok {
		t.Fatal("LoadSkill returned ok=false, want true")
	}
	// Frontmatter must be stripped; body must be present.
	if strings.Contains(content, "---") {
		t.Error("LoadSkill content still contains frontmatter delimiter '---'")
	}
	if !strings.Contains(content, "## Instructions") {
		t.Errorf("LoadSkill content missing body; got: %q", content)
	}
}

func TestLoadSkill_NotFound(t *testing.T) {
	dir := t.TempDir()
	loader := NewLoader("", dir)

	_, ok := loader.LoadSkill("nonexistent")
	if ok {
		t.Error("LoadSkill returned ok=true for nonexistent skill, want false")
	}
}

func TestLoadSkill_WorkspaceTakesPreference(t *testing.T) {
	workspaceDir := t.TempDir()
	builtinDir := t.TempDir()

	makeSkillDir(t, workspaceDir, "shared", skillMD("shared", "ws", "workspace body"))
	makeSkillDir(t, builtinDir, "shared", skillMD("shared", "bi", "builtin body"))

	loader := NewLoader(workspaceDir, builtinDir)
	content, ok := loader.LoadSkill("shared")
	if !ok {
		t.Fatal("LoadSkill returned ok=false")
	}
	if !strings.Contains(content, "workspace body") {
		t.Errorf("expected workspace body, got: %q", content)
	}
	if strings.Contains(content, "builtin body") {
		t.Errorf("got builtin body instead of workspace: %q", content)
	}
}

func TestLoadSkill_FallsBackToBuiltin(t *testing.T) {
	workspaceDir := t.TempDir()
	builtinDir := t.TempDir()

	// Only in builtin.
	makeSkillDir(t, builtinDir, "stdlib", skillMD("stdlib", "desc", "builtin content"))

	loader := NewLoader(workspaceDir, builtinDir)
	content, ok := loader.LoadSkill("stdlib")
	if !ok {
		t.Fatal("LoadSkill returned ok=false")
	}
	if !strings.Contains(content, "builtin content") {
		t.Errorf("expected builtin content, got: %q", content)
	}
}

func TestLoadSkill_StripsMultilineFrontmatter(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: my-tool\ndescription: Does stuff\n---\n\nActual skill content here.\n"
	makeSkillDir(t, dir, "my-tool", content)

	loader := NewLoader("", dir)
	body, ok := loader.LoadSkill("my-tool")
	if !ok {
		t.Fatal("LoadSkill returned ok=false")
	}
	if strings.HasPrefix(body, "---") {
		t.Error("frontmatter not stripped")
	}
	if !strings.Contains(body, "Actual skill content here.") {
		t.Errorf("body missing; got: %q", body)
	}
}

// --- BuildSummary tests ---

func TestBuildSummary_Empty(t *testing.T) {
	dir := t.TempDir()
	loader := NewLoader("", dir)
	summary := loader.BuildSummary()
	if summary != "" {
		t.Errorf("BuildSummary on empty dir: got %q, want empty string", summary)
	}
}

func TestBuildSummary_ContainsSkills(t *testing.T) {
	dir := t.TempDir()
	makeSkillDir(t, dir, "alpha", skillMD("alpha", "Alpha desc", "body"))
	makeSkillDir(t, dir, "beta", skillMD("beta", "Beta desc", "body"))

	loader := NewLoader("", dir)
	summary := loader.BuildSummary()

	for _, want := range []string{"<skills>", "alpha", "Alpha desc", "beta", "Beta desc", "</skills>"} {
		if !strings.Contains(summary, want) {
			t.Errorf("BuildSummary missing %q; got:\n%s", want, summary)
		}
	}
}

func TestBuildSummary_ContainsReadSkillHint(t *testing.T) {
	dir := t.TempDir()
	makeSkillDir(t, dir, "test-skill", skillMD("test-skill", "test", "body"))

	loader := NewLoader("", dir)
	summary := loader.BuildSummary()

	if !strings.Contains(summary, "read_skill") {
		t.Errorf("BuildSummary should mention read_skill tool; got:\n%s", summary)
	}
}

// --- parseFrontmatter tests ---

func TestParseFrontmatter_ValidFrontmatter(t *testing.T) {
	content := "---\nname: my-skill\ndescription: Does something useful\n---\n\nBody text."
	name, desc := parseFrontmatter(content)
	if name != "my-skill" {
		t.Errorf("name: got %q, want %q", name, "my-skill")
	}
	if desc != "Does something useful" {
		t.Errorf("description: got %q, want %q", desc, "Does something useful")
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	name, desc := parseFrontmatter("Just plain content with no frontmatter at all.")
	if name != "" {
		t.Errorf("name: got %q, want empty", name)
	}
	if desc != "" {
		t.Errorf("description: got %q, want empty", desc)
	}
}

func TestParseFrontmatter_PartialFrontmatter(t *testing.T) {
	// Only name, no description.
	content := "---\nname: only-name\n---\nBody."
	name, desc := parseFrontmatter(content)
	if name != "only-name" {
		t.Errorf("name: got %q, want %q", name, "only-name")
	}
	if desc != "" {
		t.Errorf("description: got %q, want empty", desc)
	}
}

// --- stripFrontmatter tests ---

func TestStripFrontmatter_WithFrontmatter(t *testing.T) {
	input := "---\nname: test\n---\n\nThe body."
	got := stripFrontmatter(input)
	if strings.Contains(got, "---") {
		t.Errorf("stripFrontmatter left delimiter; got: %q", got)
	}
	if !strings.Contains(got, "The body.") {
		t.Errorf("stripFrontmatter removed body; got: %q", got)
	}
}

func TestStripFrontmatter_WithoutFrontmatter(t *testing.T) {
	input := "No frontmatter here."
	got := stripFrontmatter(input)
	if got != input {
		t.Errorf("stripFrontmatter changed content without frontmatter; got %q, want %q", got, input)
	}
}

func TestStripFrontmatter_LeadingNewlinesRemoved(t *testing.T) {
	input := "---\nname: x\n---\n\n\n\nContent after newlines."
	got := stripFrontmatter(input)
	if strings.HasPrefix(got, "\n") {
		t.Errorf("stripFrontmatter left leading newlines; got: %q", got)
	}
	if !strings.Contains(got, "Content after newlines.") {
		t.Errorf("content missing; got: %q", got)
	}
}
