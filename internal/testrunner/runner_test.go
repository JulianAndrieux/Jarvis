package testrunner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEvents_PassingTest(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"Action":"start","Package":"p"}`,
		`{"Action":"run","Package":"p","Test":"TestFoo"}`,
		`{"Action":"output","Package":"p","Test":"TestFoo","Output":"=== RUN   TestFoo\n"}`,
		`{"Action":"pass","Package":"p","Test":"TestFoo","Elapsed":0.01}`,
		`{"Action":"pass","Package":"p","Elapsed":0.02}`,
	}, "\n"))

	got, err := parseEvents(input)
	if err != nil {
		t.Fatalf("parseEvents() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	pkg := got[0]
	if pkg.Package != "p" || pkg.Status != StatusPass {
		t.Errorf("pkg = %+v, want Package=p Status=pass", pkg)
	}
	if len(pkg.Tests) != 1 || pkg.Tests[0].Name != "TestFoo" || pkg.Tests[0].Status != StatusPass {
		t.Errorf("Tests = %+v, want [{TestFoo pass ...}]", pkg.Tests)
	}
}

func TestParseEvents_FailingTest_CapturesOutput(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"Action":"run","Package":"p","Test":"TestBad"}`,
		`{"Action":"output","Package":"p","Test":"TestBad","Output":"    file.go:3: boom\n"}`,
		`{"Action":"output","Package":"p","Test":"TestBad","Output":"--- FAIL: TestBad (0.00s)\n"}`,
		`{"Action":"fail","Package":"p","Test":"TestBad","Elapsed":0}`,
		`{"Action":"fail","Package":"p","Elapsed":0.01}`,
	}, "\n"))

	got, err := parseEvents(input)
	if err != nil {
		t.Fatalf("parseEvents() error = %v, want nil", err)
	}
	pkg := got[0]
	if pkg.Status != StatusFail {
		t.Errorf("pkg.Status = %q, want fail", pkg.Status)
	}
	if len(pkg.Tests) != 1 || pkg.Tests[0].Status != StatusFail {
		t.Fatalf("Tests = %+v, want one failing test", pkg.Tests)
	}
	if !strings.Contains(pkg.Tests[0].Output, "boom") {
		t.Errorf("Output = %q, want it to contain the failure trace", pkg.Tests[0].Output)
	}
}

func TestParseEvents_BuildFailure_NoTestEvents(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"Action":"output","Package":"p","Output":"# p\n"}`,
		`{"Action":"output","Package":"p","Output":"p/file.go:3:1: syntax error\n"}`,
		`{"Action":"fail","Package":"p","Elapsed":0}`,
	}, "\n"))

	got, err := parseEvents(input)
	if err != nil {
		t.Fatalf("parseEvents() error = %v, want nil", err)
	}
	pkg := got[0]
	if pkg.Status != StatusFail {
		t.Errorf("pkg.Status = %q, want fail", pkg.Status)
	}
	if len(pkg.Tests) != 0 {
		t.Errorf("Tests = %+v, want empty (build failed before any test ran)", pkg.Tests)
	}
	if !strings.Contains(pkg.BuildOutput, "syntax error") {
		t.Errorf("BuildOutput = %q, want it to contain the compiler error", pkg.BuildOutput)
	}
}

func TestParseEvents_MultiplePackages_SortedByName(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"Action":"pass","Package":"zzz","Elapsed":0}`,
		`{"Action":"pass","Package":"aaa","Elapsed":0}`,
	}, "\n"))

	got, err := parseEvents(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Package != "aaa" || got[1].Package != "zzz" {
		t.Errorf("got = %+v, want [aaa zzz]", got)
	}
}

func TestParseEvents_MalformedLine_ReturnsError(t *testing.T) {
	input := strings.NewReader("not json\n")
	_, err := parseEvents(input)
	if err == nil {
		t.Fatal("parseEvents() error = nil, want non-nil for malformed input")
	}
}

// --- Run() : une vraie invocation de `go test`, sur un fixture
// synthétique (aucune dépendance au module Jarvis lui-même pour ce test).

func TestRun_RealInvocation_PassAndFail(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/fixture\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(dir, "pkg", "x_test.go"), `package pkg

import "testing"

func TestOK(t *testing.T) {}
func TestNotOK(t *testing.T) { t.Fatal("boom") }
`)

	results, err := Run(context.Background(), Options{Dir: dir, Patterns: []string{"./..."}})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil (test failures are not Run errors)", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	pkg := results[0]
	if pkg.Status != StatusFail {
		t.Errorf("pkg.Status = %q, want fail (one of two tests failed)", pkg.Status)
	}
	byName := map[string]TestResult{}
	for _, tr := range pkg.Tests {
		byName[tr.Name] = tr
	}
	if byName["TestOK"].Status != StatusPass {
		t.Errorf("TestOK.Status = %q, want pass", byName["TestOK"].Status)
	}
	if byName["TestNotOK"].Status != StatusFail {
		t.Errorf("TestNotOK.Status = %q, want fail", byName["TestNotOK"].Status)
	}
	if !strings.Contains(byName["TestNotOK"].Output, "boom") {
		t.Errorf("TestNotOK.Output = %q, want it to contain the failure message", byName["TestNotOK"].Output)
	}
}

func TestRun_FilterByName(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/fixture\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(dir, "pkg", "x_test.go"), `package pkg

import "testing"

func TestOne(t *testing.T) {}
func TestTwo(t *testing.T) {}
`)

	results, err := Run(context.Background(), Options{Dir: dir, Patterns: []string{"./..."}, Run: "^TestOne$"})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if len(results) != 1 || len(results[0].Tests) != 1 || results[0].Tests[0].Name != "TestOne" {
		t.Errorf("results = %+v, want only TestOne", results)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
