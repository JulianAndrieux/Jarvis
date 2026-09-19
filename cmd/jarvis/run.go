package main

import (
	"context"
	"fmt"
	"io"
)

// run dispatche les sous-commandes. C'est le point d'entrée testable (main
// ne fait que le brancher sur os.Args/os.Stdout/os.Stderr/os.Exit).
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError()
	}

	switch args[0] {
	case "triage":
		return runTriage(ctx, args[1:], stdout)
	case "parse":
		return runParse(ctx, args[1:], stdout)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage())
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage())
	}
}

func usage() string {
	return `jarvis - pipeline local d'extraction de données PDF

Usage:
  jarvis triage <fichier.pdf>                              Détecte si le PDF a une couche texte exploitable
  jarvis parse --vlm-url URL --vlm-model NAME <fichier.pdf> Triage puis, pour les pages sans texte fiable, rendu + VLM -> Markdown
  jarvis help                                               Affiche cette aide
`
}

func usageError() error {
	return fmt.Errorf("missing command\n\n%s", usage())
}
