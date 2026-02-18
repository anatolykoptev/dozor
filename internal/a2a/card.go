package a2a

import (
	"github.com/a2aproject/a2a-go/a2a"
	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// BuildAgentCard creates the Dozor agent card from the tool registry.
func BuildAgentCard(baseURL, version string, registry *toolreg.Registry) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:               "Dozor",
		Description:        "Autonomous server monitoring and operations agent. Manages Docker Compose services, systemd units, deployments, and system resources.",
		URL:                baseURL + "/a2a",
		Version:            version,
		ProtocolVersion:    "1.0",
		PreferredTransport: a2a.TransportProtocolJSONRPC,
		Capabilities: a2a.AgentCapabilities{
			Streaming: true,
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             buildSkills(registry),
		SecuritySchemes: a2a.NamedSecuritySchemes{
			"bearer": a2a.HTTPAuthSecurityScheme{
				Scheme:      "bearer",
				Description: "Bearer token authentication",
			},
		},
		Security: []a2a.SecurityRequirements{
			{a2a.SecuritySchemeName("bearer"): a2a.SecuritySchemeScopes{}},
		},
	}
}

func buildSkills(registry *toolreg.Registry) []a2a.AgentSkill {
	// Group tools by category for the agent card.
	categories := map[string][]string{
		"monitoring":  {"server_inspect", "server_triage"},
		"operations":  {"server_restart", "server_exec", "server_remote_exec", "server_services", "server_remote"},
		"maintenance": {"server_prune", "server_cleanup", "server_updates"},
		"deployment":  {"server_deploy"},
	}

	var skills []a2a.AgentSkill
	for cat, toolNames := range categories {
		// Only include skills for tools actually registered.
		var available []string
		for _, name := range toolNames {
			if _, ok := registry.Get(name); ok {
				available = append(available, name)
			}
		}
		if len(available) == 0 {
			continue
		}

		skills = append(skills, a2a.AgentSkill{
			ID:          cat,
			Name:        cat,
			Description: "Server " + cat + " tools",
			Tags:        []string{cat, "server", "ops"},
		})
	}
	return skills
}
