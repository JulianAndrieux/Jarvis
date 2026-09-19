package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashFile_KnownContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	writeFile(t, path, []byte("hello world"))

	got, err := HashFile(path)
	if err != nil {
		t.Fatalf("HashFile() error = %v, want nil", err)
	}

	// sha256("hello world")
	want := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if got != want {
		t.Errorf("HashFile() = %q, want %q", got, want)
	}
}

func TestHashFile_SameContentSameHash(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.pdf")
	pathB := filepath.Join(dir, "b.pdf")
	writeFile(t, pathA, []byte("identical content"))
	writeFile(t, pathB, []byte("identical content"))

	hashA, err := HashFile(pathA)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := HashFile(pathB)
	if err != nil {
		t.Fatal(err)
	}
	if hashA != hashB {
		t.Errorf("HashFile(a) = %q, HashFile(b) = %q, want equal for identical content", hashA, hashB)
	}
}

func TestHashFile_DifferentContentDifferentHash(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.pdf")
	pathB := filepath.Join(dir, "b.pdf")
	writeFile(t, pathA, []byte("content A"))
	writeFile(t, pathB, []byte("content B"))

	hashA, err := HashFile(pathA)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := HashFile(pathB)
	if err != nil {
		t.Fatal(err)
	}
	if hashA == hashB {
		t.Error("HashFile(a) == HashFile(b), want different hashes for different content")
	}
}

func TestHashFile_NonExistentFile_ReturnsError(t *testing.T) {
	_, err := HashFile("does-not-exist.pdf")
	if err == nil {
		t.Fatal("HashFile() error = nil, want non-nil for a missing file")
	}
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("writeFile(%s): %v", path, err)
	}
}
