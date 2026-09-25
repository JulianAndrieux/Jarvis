package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JulianAndrieux/Jarvis/internal/tickets"
)

// stream : un flux stream-json tel que `claude -p --output-format
// stream-json --verbose` l'écrit (format relevé sur la version 2.1).
const stream = `{"type":"system","subtype":"init","cwd":"/ws/t1"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Je lis le store.\nPuis les tests."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/ws/t1/internal/webapp/store.go"}}]}}
{"type":"rate_limit_event"}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"1\tpackage webapp"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"go test ./internal/webapp\n# puis le reste","description":"Tests"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"--- FAIL: TestSince"}],"is_error":true}]}}
{"type":"system","subtype":"permission_denied","tool_name":"Bash","tool_use_id":"t9","message":"This Bash command requires approval: rm -rf x"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t3","name":"StructuredOutput","input":{"ok":true}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t3","content":"Structured output provided successfully"}]}}
{"type":"result","subtype":"success","is_error":false,"result":"Fait : filtre ajouté.","structured_output":{"ok":true},"total_cost_usd":0.42,"num_turns":4}
`

func TestParse_StepsAndResult(t *testing.T) {
	var steps []tickets.AgentStep
	res, err := parse(strings.NewReader(stream), "/ws/t1", func(s tickets.AgentStep) { steps = append(steps, s) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Fait : filtre ajouté." || string(res.Structured) != `{"ok":true}` || res.CostUSD != 0.42 || res.Turns != 4 {
		t.Errorf("result = %+v", res)
	}
	want := []string{"Claude : Je lis le store.", "Read internal/webapp/store.go", "⚠ Bash : go test ./internal/webapp", "⚠ Refusé : Bash"}
	var got []string
	for _, s := range steps {
		got = append(got, s.Summary)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("steps = %q, want %q", got, want)
	}
	if !strings.Contains(steps[3].Detail, "requires approval") {
		t.Errorf("refusal detail = %q", steps[3].Detail)
	}
	if steps[0].Detail != "Je lis le store.\nPuis les tests." || steps[1].Detail != "1\tpackage webapp" || steps[2].Detail != "--- FAIL: TestSince" {
		t.Errorf("details = %q / %q / %q", steps[0].Detail, steps[1].Detail, steps[2].Detail)
	}
}

func TestParse_Failures(t *testing.T) {
	for name, in := range map[string]string{
		"error result": `{"type":"result","subtype":"error_max_turns","is_error":true,"result":""}` + "\n",
		"no result":    `{"type":"system","subtype":"init"}` + "\n",
		"not json":     "Invalid API key · Please run /login\n",
	} {
		if _, err := parse(strings.NewReader(in), "/", func(tickets.AgentStep) {}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	_, err := parse(strings.NewReader(`{"type":"result","subtype":"error_max_turns","is_error":true}`+"\n"), "/", func(tickets.AgentStep) {})
	if err == nil || !strings.Contains(err.Error(), "error_max_turns") {
		t.Errorf("err = %v, want the subtype", err)
	}
}

func TestCLI_Args(t *testing.T) {
	c := CLI{Model: "opus"}
	args := c.args(Request{Prompt: "fais-le", System: "règles", Tools: []string{"Read", "Bash(go test:*)"}, Edit: true, Schema: []byte(`{"type":"object"}`)})
	joined := strings.Join(args, " ")
	for _, want := range []string{"-p fais-le", "--output-format stream-json", "--verbose", "--no-session-persistence", "--strict-mcp-config", "--permission-mode acceptEdits", "--allowedTools Read,Bash(go test:*)", "--append-system-prompt règles", `--json-schema {"type":"object"}`, "--model opus"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args lack %q: %q", want, joined)
		}
	}
	readOnly := strings.Join(CLI{}.args(Request{Prompt: "x", Tools: []string{"Read"}}), " ")
	for _, unwanted := range []string{"acceptEdits", "--json-schema", "--model"} {
		if strings.Contains(readOnly, unwanted) {
			t.Errorf("read-only args contain %q: %q", unwanted, readOnly)
		}
	}
	if !strings.Contains(readOnly, "--permission-mode default") {
		t.Errorf("read-only args: %q", readOnly)
	}
}

// Un faux `claude` : vérifie qu'il tourne dans le dossier demandé, sans
// entrée standard bloquante, et que sa sortie est lue.
func TestCLI_RunsTheBinaryInDir(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\ncat > /dev/null\n" +
		`printf '{"type":"result","subtype":"success","is_error":false,"result":"%s"}\n' "$(pwd -P)"` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := CLI{Binary: bin}.Run(context.Background(), Request{Dir: dir, Prompt: "x"}, func(tickets.AgentStep) {})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if res.Text != want {
		t.Errorf("ran in %q, want %q", res.Text, want)
	}

	failing := filepath.Join(t.TempDir(), "claude")
	os.WriteFile(failing, []byte("#!/bin/sh\necho 'Invalid API key' >&2\nexit 1\n"), 0o755)
	if _, err := (CLI{Binary: failing}).Run(context.Background(), Request{Dir: dir, Prompt: "x"}, func(tickets.AgentStep) {}); err == nil || !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("err = %v, want stderr in the error", err)
	}
}

// Chemins relatifs même quand Claude résout un lien symbolique du dossier
// (macOS : /var -> /private/var).
func TestToolSummary_RelativeThroughSymlinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "lien")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	resolved, _ := filepath.EvalSymlinks(real)
	got := toolSummary("Read", []byte(`{"file_path":"`+filepath.Join(resolved, "a", "b.go")+`"}`), link)
	if got != "Read a/b.go" {
		t.Errorf("summary = %q", got)
	}
}
