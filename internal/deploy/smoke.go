package deploy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// OverrideFlags remplace dans args les options nommées dans overrides
// ("--nom valeur", "--nom=valeur", un ou deux tirets) et les ajoute à la
// fin sous la forme "--nom=valeur" (valeur vide comprise), dans l'ordre
// alphabétique.
func OverrideFlags(args []string, overrides map[string]string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
			continue
		}
		if _, overridden := overrides[name]; !overridden {
			out = append(out, a)
			continue
		}
		if !hasValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++ // la valeur, en argument séparé
		}
	}
	names := make([]string, 0, len(overrides))
	for n := range overrides {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, "--"+n+"="+overrides[n])
	}
	return out
}

// FreeAddr retourne une adresse locale sur un port libre.
func FreeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("deploy: port libre : %w", err)
	}
	defer l.Close()
	return l.Addr().String(), nil
}

// Smoke essaie un binaire à blanc : il le démarre, attend que chaque page
// de Paths réponde 200 sur Addr, puis l'arrête. Args doivent le tenir à
// l'écart des vraies données (port libre, collections jetables, pas de
// dossier surveillé).
type Smoke struct {
	Args  []string
	Env   []string
	Addr  string
	Paths []string
	// Timeout borne tout l'essai (0 : 2 minutes — le démarrage analyse le
	// code du module).
	Timeout time.Duration
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Run retourne la sortie du binaire (utile en cas d'échec) et une erreur
// si une page ne répond pas 200 ou si le binaire s'arrête.
func (s Smoke) Run(ctx context.Context, binary string) (string, error) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var out lockedBuffer
	cmd := exec.Command(binary, s.Args...)
	cmd.Env = s.Env
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("deploy: essai à blanc : démarrage : %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stop := func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	for _, path := range s.Paths {
		url := "http://" + s.Addr + path
		for {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			resp, err := client.Do(req)
			if err == nil {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					stop()
					return out.String(), fmt.Errorf("deploy: essai à blanc : %s répond %d : %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
				}
				break
			}
			select {
			case err := <-exited:
				return out.String(), fmt.Errorf("deploy: essai à blanc : la nouvelle version s'est arrêtée au démarrage (%v)", err)
			case <-ctx.Done():
				stop()
				return out.String(), fmt.Errorf("deploy: essai à blanc : %s ne répond pas après %s", path, timeout)
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
	stop()
	return out.String(), nil
}
