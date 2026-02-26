package skills

import (
	"context"
	"errors"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// RegisterTools adds the read_skill tool to the registry.
func RegisterTools(registry *toolreg.Registry, loader *Loader) {
	registry.Register(&readSkillTool{loader: loader})
}

type readSkillTool struct {
	loader *Loader
}

func (t *readSkillTool) Name() string        { return "read_skill" }
func (t *readSkillTool) Description() string  { return "Load the full instructions of a skill by name" }
func (t *readSkillTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Skill name to load (e.g. 'escalation', 'diagnostics')",
			},
		},
		"required": []string{"name"},
	}
}

func (t *readSkillTool) Execute(_ context.Context, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return "", errors.New("skill name is required")
	}

	content, ok := t.loader.LoadSkill(name)
	if !ok {
		return "", fmt.Errorf("skill %q not found", name)
	}

	return content, nil
}
