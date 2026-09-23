package templates

import (
	"strings"

	"github.com/JulianAndrieux/Jarvis/internal/agents"
)

type agentGroup struct {
	Name   string
	Agents []agents.Agent
}

// agentGroups regroupe les agents par famille, dans leur ordre d'arrivée.
func agentGroups(list []agents.Agent) []agentGroup {
	var out []agentGroup
	for _, a := range list {
		if n := len(out); n > 0 && out[n-1].Name == a.Group {
			out[n-1].Agents = append(out[n-1].Agents, a)
			continue
		}
		out = append(out, agentGroup{Name: a.Group, Agents: []agents.Agent{a}})
	}
	return out
}

// promptRows : hauteur de l'éditeur, selon la longueur du prompt.
func promptRows(prompt string) int {
	n := strings.Count(prompt, "\n") + len(prompt)/100 + 3
	return min(max(n, 6), 32)
}
