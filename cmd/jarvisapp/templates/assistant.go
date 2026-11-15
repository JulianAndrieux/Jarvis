package templates

// actionSteps : ce que l'assistant a modifié (ou tenté de modifier), et
// tout ce qui a échoué — ce que l'utilisateur doit voir sans déplier.
func actionSteps(steps []AssistantStep) []AssistantStep {
	var out []AssistantStep
	for _, s := range steps {
		if s.Action || s.Failed {
			out = append(out, s)
		}
	}
	return out
}

// readSteps : les lectures réussies.
func readSteps(steps []AssistantStep) []AssistantStep {
	var out []AssistantStep
	for _, s := range steps {
		if !s.Action && !s.Failed {
			out = append(out, s)
		}
	}
	return out
}
