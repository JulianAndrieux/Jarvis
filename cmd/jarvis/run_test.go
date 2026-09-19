package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRun_NoArgs_ReturnsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), nil, &stdout, &stderr)

	if err == nil {
		t.Fatal("run() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "missing command") {
		t.Errorf("run() error = %q, want it to mention 'missing command'", err.Error())
	}
}

func TestRun_UnknownCommand_ReturnsError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"frobnicate"}, &stdout, &stderr)

	if err == nil {
		t.Fatal("run() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("run() error = %q, want it to mention the unknown command", err.Error())
	}
}

func TestRun_Help_PrintsUsageToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"help"}, &stdout, &stderr)

	if err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if !strings.Contains(stdout.String(), "jarvis triage") {
		t.Errorf("stdout = %q, want it to mention 'jarvis triage'", stdout.String())
	}
}

func TestRun_Triage_WrongArgCount_ReturnsError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"triage"}, &stdout, &stderr)

	if err == nil {
		t.Fatal("run() error = nil, want non-nil for `jarvis triage` with no path")
	}
}

func TestRun_Triage_MissingFile_ReturnsError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"triage", "does-not-exist.pdf"}, &stdout, &stderr)

	if err == nil {
		t.Fatal("run() error = nil, want non-nil for a missing file")
	}
}
