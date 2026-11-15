// Package mcp sert des outils à un modèle par le Model Context Protocol
// (JSON-RPC sur HTTP) — juste ce qu'il faut pour que Claude Code appelle
// les fonctions de Jarvis (assistant, jalon 55) : initialize, tools/list,
// tools/call. Bibliothèque standard seule. Échanges constatés sur le vrai
// CLI avant d'écrire le code : il sonde server/discover (inconnu : il
// continue), ouvre un GET de flux (405 : il s'en passe) et envoie ses
// notifications sans identifiant.
package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Tool : une fonction offerte au modèle.
type Tool struct {
	Name        string
	Description string
	// Schema : le JSON Schema des arguments (nil : objet sans propriété).
	Schema map[string]any
	// Call exécute l'outil. Une erreur est rendue au modèle comme résultat
	// en échec : il la lit et s'adapte.
	Call func(ctx context.Context, args json.RawMessage) (string, error)
}

// Server : un jeu d'outils, servi en HTTP.
type Server struct {
	Name  string
	Tools []Tool
}

// maxBody borne une requête (les arguments d'un outil sont du texte).
const maxBody = 4 << 20

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		http.Error(w, "requête illisible ou trop longue", http.StatusBadRequest)
		return
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil || req.Method == "" {
		http.Error(w, "JSON-RPC attendu", http.StatusBadRequest)
		return
	}
	// Sans identifiant : une notification, aucune réponse attendue.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rerr := s.handle(r.Context(), req)
	out := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rerr != nil {
		out["error"] = rerr
	} else {
		out["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s Server) handle(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": "1"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		tools := make([]any, 0, len(s.Tools))
		for _, t := range s.Tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema})
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		json.Unmarshal(req.Params, &p)
		for _, t := range s.Tools {
			if t.Name == p.Name {
				text, err := safeCall(ctx, t, p.Arguments)
				if err != nil {
					return toolResult("Erreur : "+err.Error(), true), nil
				}
				return toolResult(text, false), nil
			}
		}
		return nil, &rpcError{Code: -32602, Message: "outil inconnu : " + p.Name}
	}
	return nil, &rpcError{Code: -32601, Message: "méthode inconnue : " + req.Method}
}

// safeCall : un outil qui panique rend une erreur au modèle, il n'emporte
// pas le processus.
func safeCall(ctx context.Context, t Tool, args json.RawMessage) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("l'outil %s a échoué : %v", t.Name, r)
		}
	}()
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return t.Call(ctx, args)
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isError}
}

// Hub sert plusieurs sessions d'outils à la même adresse, chacune derrière
// son jeton : une session n'existe que le temps d'une réponse de
// l'assistant, et ses outils agissent avec la portée de celui qui a posé
// la question — le jeton est donc ce qui tient lieu de session.
type Hub struct {
	mu       sync.Mutex
	sessions map[string]Server // clé : SHA-256 du jeton
}

func NewHub() *Hub { return &Hub{sessions: map[string]Server{}} }

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Open ouvre une session et rend son jeton ; closeFn la ferme (le jeton ne
// vaut alors plus rien).
func (h *Hub) Open(s Server) (token string, closeFn func(), err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("mcp: jeton : %w", err)
	}
	token = hex.EncodeToString(raw)
	key := hashToken(token)
	h.mu.Lock()
	h.sessions[key] = s
	h.mu.Unlock()
	return token, func() {
		h.mu.Lock()
		delete(h.sessions, key)
		h.mu.Unlock()
	}, nil
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		http.Error(w, "jeton requis", http.StatusUnauthorized)
		return
	}
	h.mu.Lock()
	s, found := h.sessions[hashToken(token)]
	h.mu.Unlock()
	if !found {
		http.Error(w, "session inconnue ou fermée", http.StatusUnauthorized)
		return
	}
	s.ServeHTTP(w, r)
}

// Listen sert le Hub sur un port libre de 127.0.0.1 — à part de
// l'application : ni cookie de session ni reverse proxy n'ont à connaître
// cette adresse, seul le processus du modèle, lancé par Jarvis, la reçoit.
// Rend l'adresse à donner au client ; s'arrête avec ctx.
func (h *Hub) Listen(ctx context.Context) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("mcp: écoute : %w", err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	return "http://" + ln.Addr().String() + "/mcp", nil
}
