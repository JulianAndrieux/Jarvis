package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func baseProcessArgs() []string {
	return []string{
		"process",
		"--vlm-url", "http://localhost:8080/v1",
		"--vlm-model", "olmOCR-2-7B-1025",
		"--llm-url", "http://localhost:8081/v1",
		"--llm-model", "qwen3-8b",
		"--doc-type", "facture",
	}
}

func TestRun_Process_WrongArgCount_ReturnsError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), baseProcessArgs(), &stdout, &stderr)
	if err == nil {
		t.Fatal("run() error = nil, want non-nil for `jarvis process` with no path")
	}
}

func TestRun_Process_MissingFile_ReturnsError(t *testing.T) {
	args := append(baseProcessArgs(), "does-not-exist.pdf")
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), args, &stdout, &stderr)
	if err == nil {
		t.Fatal("run() error = nil, want non-nil for a missing file")
	}
}

func TestRun_Process_RequiresVLMURLFlag(t *testing.T) {
	args := []string{"process", "--vlm-model", "m", "--llm-url", "u", "--llm-model", "m", "--doc-type", "facture", "some.pdf"}
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), args, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "vlm-url") {
		t.Fatalf("run() error = %v, want it to mention the missing --vlm-url flag", err)
	}
}

func TestRun_Process_RequiresVLMModelFlag(t *testing.T) {
	args := []string{"process", "--vlm-url", "u", "--llm-url", "u", "--llm-model", "m", "--doc-type", "facture", "some.pdf"}
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), args, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "vlm-model") {
		t.Fatalf("run() error = %v, want it to mention the missing --vlm-model flag", err)
	}
}

func TestRun_Process_RequiresLLMURLFlag(t *testing.T) {
	args := []string{"process", "--vlm-url", "u", "--vlm-model", "m", "--llm-model", "m", "--doc-type", "facture", "some.pdf"}
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), args, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "llm-url") {
		t.Fatalf("run() error = %v, want it to mention the missing --llm-url flag", err)
	}
}

func TestRun_Process_RequiresLLMModelFlag(t *testing.T) {
	args := []string{"process", "--vlm-url", "u", "--vlm-model", "m", "--llm-url", "u", "--doc-type", "facture", "some.pdf"}
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), args, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "llm-model") {
		t.Fatalf("run() error = %v, want it to mention the missing --llm-model flag", err)
	}
}

func TestRun_Process_RequiresDocTypeFlag(t *testing.T) {
	args := []string{"process", "--vlm-url", "u", "--vlm-model", "m", "--llm-url", "u", "--llm-model", "m", "some.pdf"}
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), args, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "doc-type") {
		t.Fatalf("run() error = %v, want it to mention the missing --doc-type flag", err)
	}
}

func TestRun_Process_UnknownDocType_ReturnsError(t *testing.T) {
	args := []string{"process", "--vlm-url", "u", "--vlm-model", "m", "--llm-url", "u", "--llm-model", "m", "--doc-type", "extraterrestre", "some.pdf"}
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), args, &stdout, &stderr)
	if err == nil {
		t.Fatal("run() error = nil, want non-nil for an unregistered doc-type")
	}
	if !strings.Contains(err.Error(), "extraterrestre") {
		t.Errorf("run() error = %v, want it to mention the unknown doc-type", err)
	}
}
