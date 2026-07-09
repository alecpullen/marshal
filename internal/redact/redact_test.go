package redact

import "testing"

func TestSecretsMasksAWSKey(t *testing.T) {
	in := "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY and more"
	out := Secrets(in)
	if out == in {
		t.Fatal("AWS secret not redacted")
	}
	if contains(out, "wJalrXUtnFEMI") {
		t.Fatalf("secret value survived redaction: %q", out)
	}
}

func TestSecretsMasksGenericKeyAssignments(t *testing.T) {
	in := "OPENAI_API_KEY=sk-proj-1234567890abcdefGHIJ and GITHUB_TOKEN=ghp_abcdef1234"
	out := Secrets(in)
	if contains(out, "sk-proj-1234567890abcdef") || contains(out, "ghp_abcdef1234") {
		t.Fatalf("token values survived: %q", out)
	}
}

func TestSecretsLeavesNormalText(t *testing.T) {
	in := "The function parseAction lives in internal/agent/protocol.go."
	out := Secrets(in)
	if out != in {
		t.Fatalf("normal text altered: %q", out)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
