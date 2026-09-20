package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testConfig() Config {
	cfg := DefaultConfig("/home/andri", "/home/andri/Documents/Jarvis")
	cfg.MongoURI = "mongodb+srv://user:pass@cluster/"
	return cfg
}

func TestArgsForVLM_IncludesModelMMProjAndPort(t *testing.T) {
	args := ArgsForVLM(testConfig())
	want := []string{
		"-m", "/home/andri/models/olmocr2/olmOCR-2-7B-1025-Q6_K.gguf",
		"--mmproj", "/home/andri/models/olmocr2/mmproj-olmOCR-2-7B-1025-F16.gguf",
		"--host", "127.0.0.1",
		"--port", "8080",
		"--ctx-size", "8192",
		"--image-min-tokens", "1024",
	}
	assertArgsEqual(t, args, want)
}

func TestArgsForLLM_IncludesModelAndPort(t *testing.T) {
	args := ArgsForLLM(testConfig())
	want := []string{
		"-m", "/home/andri/models/qwen3-8b/Qwen3-8B-Q5_K_M.gguf",
		"--host", "127.0.0.1",
		"--port", "8081",
		"--ctx-size", "8192",
	}
	assertArgsEqual(t, args, want)
}

func TestArgsForJarvisApp_IncludesAllRequiredFlags(t *testing.T) {
	args := ArgsForJarvisApp(testConfig())
	want := []string{
		"--vlm-url", "http://127.0.0.1:8080/v1",
		"--vlm-model", "olmOCR-2-7B-1025",
		"--vlm-model-version", "Q6_K",
		"--llm-url", "http://127.0.0.1:8081/v1",
		"--llm-model", "qwen3-8b",
		"--llm-model-version", "Q5_K_M",
		"--mongo-uri", "mongodb+srv://user:pass@cluster/",
		"--mongo-db", "jarvis",
		"--mongo-collection", "jobs",
		"--out-dir", "/home/andri/Documents/Jarvis/data/results",
		"--addr", "127.0.0.1:8090",
		"--module-dir", "/home/andri/Documents/Jarvis",
	}
	assertArgsEqual(t, args, want)
}

func TestArgsForJarvisApp_EmptyMongoURI_OmitsNothingButLeavesItEmpty(t *testing.T) {
	cfg := testConfig()
	cfg.MongoURI = ""
	args := ArgsForJarvisApp(cfg)
	if !containsFlag(args, "--mongo-uri", "") {
		t.Errorf("args = %v, want --mongo-uri \"\" present (jarvisapp itself refuses to start without it, with a clear message)", args)
	}
}

func assertArgsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q (full: got=%v want=%v)", i, got[i], want[i], got, want)
		}
	}
}

func containsFlag(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestResolvePath_RelativeJoinsWithRepoDir(t *testing.T) {
	got := ResolvePath("/repo", "bin/jarvisapp")
	if got != "/repo/bin/jarvisapp" {
		t.Errorf("ResolvePath() = %q, want %q", got, "/repo/bin/jarvisapp")
	}
}

func TestResolvePath_AbsoluteIsReturnedUnchanged(t *testing.T) {
	got := ResolvePath("/repo", "/elsewhere/jarvisapp")
	if got != "/elsewhere/jarvisapp" {
		t.Errorf("ResolvePath() = %q, want %q", got, "/elsewhere/jarvisapp")
	}
}

func TestStartProcess_RunsAndCapturesOutputToLogFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "sub", "echo.log")

	cmd, err := StartProcess("/bin/echo", []string{"hello-from-launcher"}, dir, logPath)
	if err != nil {
		t.Fatalf("StartProcess() error = %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cmd.Wait() error = %v", err)
	}

	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(b), "hello-from-launcher") {
		t.Errorf("log content = %q, want it to contain the process output", b)
	}
}

func TestStartProcess_UnknownBinary_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	_, err := StartProcess("/no/such/binary-xyz", nil, dir, filepath.Join(dir, "x.log"))
	if err == nil {
		t.Fatal("StartProcess() error = nil, want an error for a missing binary")
	}
}
