package launcher

import "testing"

func TestParseOSAScriptTextReturned_ExtractsTypedText(t *testing.T) {
	got, err := ParseOSAScriptTextReturned("button returned:OK, text returned:mongodb+srv://user:pass@cluster/\n")
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if got != "mongodb+srv://user:pass@cluster/" {
		t.Errorf("got = %q, want the URI", got)
	}
}

func TestParseOSAScriptTextReturned_UnexpectedOutput_ReturnsError(t *testing.T) {
	_, err := ParseOSAScriptTextReturned("something unexpected")
	if err == nil {
		t.Fatal("error = nil, want an error for output with no \"text returned:\" marker")
	}
}
