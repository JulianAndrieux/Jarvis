package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/JulianAndrieux/Jarvis/internal/deploy"
)

// Relecture visuelle (jalon 38) : la version du ticket et la version en
// service sont lancées côte à côte, isolées, le temps des captures.

// visualArgs : les options d'une version lancée pour une capture — celles
// du processus courant, mais port libre, collections jetables, et rien qui
// agisse (modèles, dossier surveillé, copie locale, tickets).
func visualArgs(current []string, addr, jobsCollection, ticketsCollection string) []string {
	return deploy.OverrideFlags(current, map[string]string{
		"addr":               addr,
		"mongo-collection":   jobsCollection + "_visualcheck",
		"tickets-collection": ticketsCollection + "_visualcheck",
		"models-file":        "",
		"watch-dir":          "",
		"out-dir":            "",
		"agent-dev":          "false",
		"deploy":             "false",
		"deploy-marker":      filepath.Join(os.TempDir(), "jarvis-visualcheck.json"),
	})
}

// launchIsolated démarre binary pour des captures et attend qu'il réponde.
func launchIsolated(jobsCollection, ticketsCollection string) func(ctx context.Context, binary string) (string, func(), error) {
	return func(ctx context.Context, binary string) (string, func(), error) {
		addr, err := deploy.FreeAddr()
		if err != nil {
			return "", nil, err
		}
		cmd := exec.Command(binary, visualArgs(os.Args[1:], addr, jobsCollection, ticketsCollection)...)
		cmd.Env = os.Environ()
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return "", nil, err
		}
		stop := func() {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			done := make(chan struct{})
			go func() { cmd.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				<-done
			}
		}
		base := "http://" + addr
		deadline := time.Now().Add(90 * time.Second)
		for {
			if resp, err := http.Get(base + "/"); err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return base, stop, nil
				}
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				stop()
				return "", nil, fmt.Errorf("la version %s ne répond pas", filepath.Base(binary))
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
}
