package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRun_Parse_WrongArgCount_ReturnsError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"parse"}, &stdout, &stderr)

	if err == nil {
		t.Fatal("run() error = nil, want non-nil for `jarvis parse` with no path")
	}
}

func TestRun_Parse_MissingFile_ReturnsError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"parse", "does-not-exist.pdf"}, &stdout, &stderr)

	if err == nil {
		t.Fatal("run() error = nil, want non-nil for a missing file")
	}
}

func TestRun_Parse_RequiresVLMURLFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// Pas de --vlm-url : jarvis ne doit pas inventer une valeur par défaut
	// silencieuse pointant vers un serveur qui n'existe peut-être pas.
	err := run(context.Background(), []string{"parse", "--vlm-model", "m", "some.pdf"}, &stdout, &stderr)

	if err == nil {
		t.Fatal("run() error = nil, want non-nil when --vlm-url is missing")
	}
	if !strings.Contains(err.Error(), "vlm-url") {
		t.Errorf("run() error = %q, want it to mention the missing --vlm-url flag", err.Error())
	}
}

func TestRun_Parse_RequiresVLMModelFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// Pas de --vlm-model : on refuse plutôt que d'écrire une provenance
	// vide/trompeuse (nom de modèle requis pour la reproductibilité).
	err := run(context.Background(), []string{"parse", "--vlm-url", "http://localhost:8080/v1", "some.pdf"}, &stdout, &stderr)

	if err == nil {
		t.Fatal("run() error = nil, want non-nil when --vlm-model is missing")
	}
	if !strings.Contains(err.Error(), "vlm-model") {
		t.Errorf("run() error = %q, want it to mention the missing --vlm-model flag", err.Error())
	}
}
