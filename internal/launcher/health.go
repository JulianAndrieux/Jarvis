package launcher

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// PortOpen tente une connexion TCP à addr (host:port) et retourne true
// si quelque chose écoute déjà — utilisé pour ne pas démarrer un second
// serveur si un llama-server tourne déjà manuellement sur ce port.
func PortOpen(addr string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// WaitHealthy interroge url (GET) jusqu'à recevoir un 200, en
// réessayant toutes les poll, jusqu'à ce que ctx expire — auquel cas
// l'erreur du dernier essai (ou celle du contexte) est retournée.
// Utilisé après avoir démarré un serveur pour attendre qu'il soit
// vraiment prêt avant de continuer, plutôt qu'un simple `sleep` fixe.
func WaitHealthy(ctx context.Context, url string, poll time.Duration) error {
	client := &http.Client{Timeout: poll}
	var lastErr error

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			resp, doErr := client.Do(req)
			if doErr == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
				lastErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
			} else {
				lastErr = doErr
			}
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("launcher: %s never became healthy: %w (last error: %v)", url, ctx.Err(), lastErr)
		case <-time.After(poll):
		}
	}
}
