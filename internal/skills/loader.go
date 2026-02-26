package skills

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// dirPermissions is the permission mode for created workspace directories.
	dirPermissions = 0750
	// filePermissions is the permission mode for created skill files.
	filePermissions = 0600
	// yamlFieldMinParts is the minimum number of parts in a YAML field match.
	yamlFieldMinParts = 3
)

// SkillInfo holds metadata about a discovered skill.
type SkillInfo struct {
	Name        string
	Description string
	Path        string
	Source      string // "workspace" or "builtin"
}

// Loader discovers and loads skills from the filesystem.
type Loader struct {
	workspaceDir string // user skills (higher priority)
	builtinDir   string // repo skills (lower priority)
}

var reFrontmatter = regexp.MustCompile(`(?s)^---\n(.*?)\n---`)
var reYAMLField = regexp.MustCompile(`(?m)^(\w+):\s*"?(.+?)"?\s*$`)

// NewLoader creates a skills loader.
// workspaceDir: user-customizable skills (e.g. ~/.dozor/skills/)
// builtinDir: shipped with binary (e.g. ./skills/)
func NewLoader(workspaceDir, builtinDir string) *Loader {
	return &Loader{
		workspaceDir: workspaceDir,
		builtinDir:   builtinDir,
	}
}

// ListSkills discovers all skills from workspace and builtin directories.
// Workspace skills override builtin skills with the same name.
func (l *Loader) ListSkills() []SkillInfo {
	seen := make(map[string]bool)
	var skills []SkillInfo

	// Workspace first (higher priority).
	if l.workspaceDir != "" {
		for _, si := range l.scanDir(l.workspaceDir, "workspace") {
			seen[si.Name] = true
			skills = append(skills, si)
		}
	}

	// Builtin second (lower priority, skip duplicates).
	if l.builtinDir != "" {
		for _, si := range l.scanDir(l.builtinDir, "builtin") {
			if !seen[si.Name] {
				skills = append(skills, si)
			}
		}
	}

	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Name < skills[j].Name
	})
	return skills
}

// LoadSkill reads the full SKILL.md content (with frontmatter stripped).
func (l *Loader) LoadSkill(name string) (string, bool) {
	// Try workspace first.
	if l.workspaceDir != "" {
		if content, ok := l.readSkill(l.workspaceDir, name); ok {
			return content, true
		}
	}
	// Then builtin.
	if l.builtinDir != "" {
		if content, ok := l.readSkill(l.builtinDir, name); ok {
			return content, true
		}
	}
	return "", false
}

// BuildSummary generates an XML summary of all skills for the system prompt.
func (l *Loader) BuildSummary() string {
	skills := l.ListSkills()
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<skills>\n")
	for _, si := range skills {
		fmt.Fprintf(&sb, "  <skill name=%q source=%q>\n", si.Name, si.Source)
		fmt.Fprintf(&sb, "    <description>%s</description>\n", si.Description)
		fmt.Fprintf(&sb, "  </skill>\n")
	}
	sb.WriteString("</skills>\n")
	sb.WriteString("To load a skill's full instructions, use the read_skill tool with the skill name.\n")
	return sb.String()
}

func (l *Loader) scanDir(dir, source string) []SkillInfo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var skills []SkillInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillFile := filepath.Join(dir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}

		name, desc := parseFrontmatter(string(data))
		if name == "" {
			name = e.Name()
		}

		skills = append(skills, SkillInfo{
			Name:        name,
			Description: desc,
			Path:        skillFile,
			Source:      source,
		})
	}
	return skills
}

func (l *Loader) readSkill(dir, name string) (string, bool) {
	skillFile := filepath.Join(dir, name, "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		return "", false
	}
	return stripFrontmatter(string(data)), true
}

// parseFrontmatter extracts name and description from YAML frontmatter.
func parseFrontmatter(content string) (name, description string) {
	match := reFrontmatter.FindStringSubmatch(content)
	if len(match) < yamlFieldMinParts-1 {
		return "", ""
	}

	fields := reYAMLField.FindAllStringSubmatch(match[1], -1)
	for _, f := range fields {
		if len(f) < yamlFieldMinParts {
			continue
		}
		switch f[1] {
		case "name":
			name = f[2]
		case "description":
			description = f[2]
		}
	}
	return name, description
}

// stripFrontmatter removes the YAML frontmatter from content.
func stripFrontmatter(content string) string {
	loc := reFrontmatter.FindStringIndex(content)
	if loc == nil {
		return content
	}
	stripped := content[loc[1]:]
	return strings.TrimLeft(stripped, "\n")
}

// InitWorkspace creates the workspace directory with default files if it doesn't exist.
func InitWorkspace(workspacePath, defaultsPath string) {
	if err := os.MkdirAll(workspacePath, dirPermissions); err != nil {
		slog.Warn("cannot create workspace", slog.Any("error", err))
		return
	}

	// Copy default bootstrap files if they don't exist yet.
	defaults := []string{"IDENTITY.md", "AGENTS.md", "MEMORY.md"}
	for _, name := range defaults {
		dest := filepath.Join(workspacePath, name)
		if _, err := os.Stat(dest); err == nil {
			continue // already exists, don't overwrite
		}

		src := filepath.Join(defaultsPath, name)
		data, err := os.ReadFile(src)
		if err != nil {
			slog.Debug("default file not found", slog.String("file", src))
			continue
		}

		if err := os.WriteFile(dest, data, filePermissions); err != nil {
			slog.Warn("cannot write bootstrap file", slog.String("file", dest), slog.Any("error", err))
		} else {
			slog.Info("initialized bootstrap file", slog.String("file", dest))
		}
	}

	// Create user skills directory.
	skillsDir := filepath.Join(workspacePath, "skills")
	if err := os.MkdirAll(skillsDir, dirPermissions); err != nil {
		slog.Warn("cannot create skills directory", slog.String("dir", skillsDir), slog.Any("error", err))
	}
}
