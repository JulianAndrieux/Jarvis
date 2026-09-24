package models

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProcessRunner démarre les serveurs llama.cpp en processus détachés et
// les arrête par leur port (lsof) : un serveur lancé à la main ou par un
// démarrage précédent de Jarvis est géré de la même façon.
type ProcessRunner struct {
	Binary string // llama-server
	LogDir string
}

func (r ProcessRunner) Start(s Server) error {
	if err := os.MkdirAll(r.LogDir, 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(r.LogDir, s.Name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(r.Binary, s.Args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	// Son propre groupe : il survit à un redémarrage de jarvisapp
	// (déploiement) et n'est pas tué avec lui.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return err
	}
	go func() { cmd.Wait(); logFile.Close() }()
	return nil
}

// Stop arrête ce qui écoute sur port : SIGTERM, puis SIGKILL au bout de
// 15 s si le port n'est pas libéré.
func (r ProcessRunner) Stop(port int) error {
	pids, err := listeners(port)
	if err != nil || len(pids) == 0 {
		return err
	}
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if left, _ := listeners(port); len(left) == 0 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}

// listeners : les processus qui écoutent sur port.
func listeners(port int) ([]int, error) {
	out, err := exec.Command("lsof", "-t", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil // rien n'écoute
		}
		return nil, fmt.Errorf("lsof :%d : %w", port, err)
	}
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(f); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// Healthy : /health répond 200 (llama-server répond 503 tant que le
// modèle charge).
func (r ProcessRunner) Healthy(ctx context.Context, port int) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health", port), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
