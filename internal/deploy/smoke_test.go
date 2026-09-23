package deploy

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestMain : le binaire de test sert aussi de "nouvelle version" factice
// pour l'essai à blanc (JARVIS_SMOKE_HELPER), comme le fait os/exec.
func TestMain(m *testing.M) {
	switch os.Getenv("JARVIS_SMOKE_HELPER") {
	case "serve":
		addr := ""
		for _, a := range os.Args {
			if v, ok := strings.CutPrefix(a, "--addr="); ok {
				addr = v
			}
		}
		http.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) })
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
		fmt.Println("helper: listening on", addr)
		http.ListenAndServe(addr, nil)
		os.Exit(0)
	case "crash":
		fmt.Fprintln(os.Stderr, "helper: panic: configuration invalide")
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func TestOverrideFlags(t *testing.T) {
	args := []string{"--addr", "127.0.0.1:8090", "--mongo-collection=jobs", "--watch-dir", "/w", "-dpi", "200", "--agent-dev"}
	got := OverrideFlags(args, map[string]string{"addr": "127.0.0.1:9999", "mongo-collection": "jobs_x", "watch-dir": "", "out-dir": ""})
	want := []string{"-dpi", "200", "--agent-dev", "--addr=127.0.0.1:9999", "--mongo-collection=jobs_x", "--out-dir=", "--watch-dir="}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OverrideFlags() =\n %q\nwant\n %q", got, want)
	}
}

func helperEnv(mode string) []string { return append(os.Environ(), "JARVIS_SMOKE_HELPER="+mode) }

func TestSmoke_PassesWhenPagesRespond(t *testing.T) {
	addr, err := FreeAddr()
	if err != nil {
		t.Fatal(err)
	}
	s := Smoke{Args: []string{"--addr=" + addr}, Env: helperEnv("serve"), Addr: addr, Paths: []string{"/", "/documents"}, Timeout: 20 * time.Second}
	if out, err := s.Run(context.Background(), os.Args[0]); err != nil {
		t.Fatalf("Smoke.Run() = %v\n%s", err, out)
	}
	// Le processus d'essai est arrêté après coup.
	time.Sleep(200 * time.Millisecond)
	if _, err := http.Get("http://" + addr + "/"); err == nil {
		t.Error("smoke process still running after the check")
	}
}

func TestSmoke_FailsOnBrokenPageOrCrash(t *testing.T) {
	addr, _ := FreeAddr()
	s := Smoke{Args: []string{"--addr=" + addr}, Env: helperEnv("serve"), Addr: addr, Paths: []string{"/", "/broken"}, Timeout: 20 * time.Second}
	if _, err := s.Run(context.Background(), os.Args[0]); err == nil || !strings.Contains(err.Error(), "/broken") {
		t.Errorf("broken page: err = %v", err)
	}

	addr, _ = FreeAddr()
	s = Smoke{Args: []string{"--addr=" + addr}, Env: helperEnv("crash"), Addr: addr, Paths: []string{"/"}, Timeout: 20 * time.Second}
	start := time.Now()
	out, err := s.Run(context.Background(), os.Args[0])
	if err == nil || !strings.Contains(out, "configuration invalide") {
		t.Errorf("crash: err = %v, output = %q (the output explains why)", err, out)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("a crash must be reported at once, not after the timeout")
	}
}
