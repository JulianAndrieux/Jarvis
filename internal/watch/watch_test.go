package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWatcher_Tick_ProcessesExistingPDFFilesAndMovesToProcessed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.pdf", "contenu-a")
	writeFile(t, dir, "b.pdf", "contenu-b")

	var mu sync.Mutex
	got := map[string]string{}
	w := &Watcher{Dir: dir, OnFile: func(ctx context.Context, filename string, content []byte) error {
		mu.Lock()
		defer mu.Unlock()
		got[filename] = string(content)
		return nil
	}}

	w.tick(context.Background())

	if len(got) != 2 || got["a.pdf"] != "contenu-a" || got["b.pdf"] != "contenu-b" {
		t.Fatalf("got = %v, want both files with their content", got)
	}

	for _, name := range []string{"a.pdf", "b.pdf"} {
		if _, err := os.Stat(filepath.Join(dir, "processed", name)); err != nil {
			t.Errorf("%s not found in processed/: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s still present in watched dir, want it moved away", name)
		}
	}
}

func TestWatcher_Tick_IgnoresNonPDFFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "notes.txt", "pas un pdf")

	called := false
	w := &Watcher{Dir: dir, OnFile: func(ctx context.Context, filename string, content []byte) error {
		called = true
		return nil
	}}

	w.tick(context.Background())

	if called {
		t.Error("OnFile was called for a non-PDF file")
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Errorf("notes.txt should remain untouched: %v", err)
	}
}

func TestWatcher_Tick_IgnoresSubdirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "processed"), 0o755); err != nil {
		t.Fatal(err)
	}

	called := false
	w := &Watcher{Dir: dir, OnFile: func(ctx context.Context, filename string, content []byte) error {
		called = true
		return nil
	}}

	w.tick(context.Background())

	if called {
		t.Error("OnFile was called for a subdirectory entry")
	}
}

func TestWatcher_Tick_OnFileError_MovesToFailed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bad.pdf", "contenu")

	w := &Watcher{Dir: dir, OnFile: func(ctx context.Context, filename string, content []byte) error {
		return errors.New("submit boom")
	}}

	w.tick(context.Background())

	if _, err := os.Stat(filepath.Join(dir, "failed", "bad.pdf")); err != nil {
		t.Errorf("bad.pdf not found in failed/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "processed", "bad.pdf")); !os.IsNotExist(err) {
		t.Error("bad.pdf should not be in processed/")
	}
}

func TestWatcher_Tick_NameCollisionInProcessed_DoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "processed"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "processed"), "dup.pdf", "premier-passage")
	writeFile(t, dir, "dup.pdf", "second-passage")

	w := &Watcher{Dir: dir, OnFile: func(ctx context.Context, filename string, content []byte) error {
		return nil
	}}

	w.tick(context.Background())

	original, err := os.ReadFile(filepath.Join(dir, "processed", "dup.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != "premier-passage" {
		t.Errorf("existing processed/dup.pdf was overwritten: %q", original)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "processed"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("processed/ has %d entries, want 2 (original + renamed second passage)", len(entries))
	}
}

func TestWatcher_Run_StopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	w := &Watcher{Dir: dir, Interval: time.Hour, OnFile: func(ctx context.Context, filename string, content []byte) error {
		return nil
	}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Run() error = nil, want context.Canceled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after context cancellation")
	}
}

func TestWatcher_Run_ProcessesFilesFoundImmediately(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "immediate.pdf", "contenu")

	called := make(chan struct{}, 1)
	w := &Watcher{Dir: dir, Interval: time.Hour, OnFile: func(ctx context.Context, filename string, content []byte) error {
		called <- struct{}{}
		return nil
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("OnFile was not called promptly at startup (want the first tick to run immediately, not after Interval)")
	}
}
