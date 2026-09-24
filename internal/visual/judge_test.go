package visual

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Le juge envoie le besoin et les captures (avant, après) en images, avec
// un schéma JSON imposé, et lit le verdict.
func TestVisionJudge(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &got)
		reply := `{"verdict": "a_reprendre", "summary": "Les dates sont à droite.", "issues": [{"page": "/documents", "severity": "important", "message": "Les champs de date doivent être sous la barre."}]}`
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reply}}}})
		w.Write(b)
	}))
	defer srv.Close()

	j := VisionJudge{BaseURL: srv.URL + "/v1", Model: "devstral"}
	res, err := j.Judge(context.Background(), JudgeRequest{
		Title: "Déplacer le filtre", Need: "Sous la barre de recherche",
		Captures: []Capture{{Page: "/documents", Before: []byte("PNG-AVANT"), After: []byte("PNG-APRES")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Approved || res.Summary != "Les dates sont à droite." || len(res.Issues) != 1 || res.Issues[0].File != "capture /documents" || res.Issues[0].Severity != "important" {
		t.Errorf("result = %+v", res)
	}
	raw, _ := json.Marshal(got)
	s := string(raw)
	for _, want := range []string{`"image_url"`, "data:image/png;base64,", "Sous la barre de recherche", `"json_schema"`, `"verdict"`} {
		if !strings.Contains(s, want) {
			t.Errorf("request lacks %q", want)
		}
	}
	if strings.Count(s, "data:image/png;base64,") != 2 {
		t.Errorf("want the before and after captures sent")
	}
}

func TestVisionJudge_BadReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"pas du JSON"}}]}`))
	}))
	defer srv.Close()
	if _, err := (VisionJudge{BaseURL: srv.URL}).Judge(context.Background(), JudgeRequest{Captures: []Capture{{Page: "/"}}}); err == nil {
		t.Error("unreadable verdict accepted")
	}
}
