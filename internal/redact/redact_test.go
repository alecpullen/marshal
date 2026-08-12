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

func TestSecretsMasksGenericSecretKeyNames(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		secret string
	}{
		{"DB_PASSWORD", "DB_PASSWORD=hunter2", "hunter2"},
		{"API_KEY suffix", "MY_API_KEY=sk-proj-1234567890abcdef", "sk-proj-1234567890abcdef"},
		{"TOKEN suffix", "SLACK_TOKEN=xoxb-1234567890-abc", "xoxb-1234567890-abc"},
		{"SECRET substring", "CLIENT_SECRET=abcdef123", "abcdef123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Secrets(tc.input)
			if contains(out, tc.secret) {
				t.Fatalf("secret value survived: %q", out)
			}
			if !contains(out, MaskToken) {
				t.Fatalf("redaction marker absent: %q", out)
			}
		})
	}
}

func TestSecretsMasksAdditionalTokenSigils(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		secret string
	}{
		{"github oauth", "token is gho_abcdefghijklmnopqrst", "gho_abcdefghijklmnopqrst"},
		{"github user", "token is ghu_abcdefghijklmnopqrst", "ghu_abcdefghijklmnopqrst"},
		{"github server", "token is ghs_abcdefghijklmnopqrst", "ghs_abcdefghijklmnopqrst"},
		{"github pat", "token is github_pat_abc123def456ghi7890123456", "github_pat_abc123def456ghi7890123456"},
		{"slack bot", "token is xoxb-1234567890-ABCDEF", "xoxb-1234567890-ABCDEF"},
		{"bearer jwt", "Authorization: Bearer eyJhbGciOiJIUz.eyJzdWIiOiIxMjM.SflKxwRJSMeKKF2QT4f", "eyJhbGciOiJIUz.eyJzdWIiOiIxMjM.SflKxwRJSMeKKF2QT4f"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Secrets(tc.input)
			if contains(out, tc.secret) {
				t.Fatalf("token survived: %q", out)
			}
			if !contains(out, MaskToken) {
				t.Fatalf("redaction marker absent: %q", out)
			}
		})
	}
}

// TestSecretsMasksVendorTokensWithoutAssignment covers sigils that used to
// pass through as bare tokens. Stripe separates its prefix with an
// underscore, so the sk- branch never matched it, and Google's AIza keys had
// no branch at all. Both were only caught in KEY=value form.
func TestSecretsMasksVendorTokensWithoutAssignment(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		secret string
	}{
		{"stripe secret", "charge failed for sk_live_51ABCdefGHIjklMNOpqrSTU", "sk_live_51ABCdefGHIjklMNOpqrSTU"},
		{"stripe test", "using sk_test_51ABCdefGHIjklMNOpqrSTU here", "sk_test_51ABCdefGHIjklMNOpqrSTU"},
		{"stripe restricted", "using rk_live_51ABCdefGHIjklMNOpqrSTU here", "rk_live_51ABCdefGHIjklMNOpqrSTU"},
		{"stripe webhook", "signing with whsec_ABCdefGHIjklMNOpqrSTU", "whsec_ABCdefGHIjklMNOpqrSTU"},
		// Exactly 39 characters, the real Google API key length.
		{"google api key", "maps call used AIzaSyD1234567890abcdefghijklmnopqrstuv", "AIzaSyD1234567890abcdefghijklmnopqrstuv"},
		// One character longer: must still mask, not fall through.
		{"google api key overlong", "key AIzaSyD1234567890abcdefghijklmnopqrstuvw here", "AIzaSyD1234567890abcdefghijklmnopqrstuvw"},
		{"slack user", "token is xoxp-1234567890-ABCDEF", "xoxp-1234567890-ABCDEF"},
		{"slack app", "token is xoxa-1234567890-ABCDEF", "xoxa-1234567890-ABCDEF"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Secrets(tc.input)
			if contains(out, tc.secret) {
				t.Fatalf("token survived: %q", out)
			}
			if !contains(out, MaskToken) {
				t.Fatalf("redaction marker absent: %q", out)
			}
		})
	}
}

// TestSecretsLeavesPublishableAndProseAlone guards the other direction: the
// new branches must not mask values that are meant to be readable.
func TestSecretsLeavesPublishableAndProseAlone(t *testing.T) {
	cases := []string{
		// Stripe publishable keys are designed to ship in client code.
		"the publishable key pk_live_51ABCdefGHIjklMNOpqrSTU is fine to commit",
		// Too short to be a Google key (35 payload chars required).
		"identifier AIzaShort123 is not a credential",
		"see internal/redact/redact.go for the sk- prefix handling",
	}
	for _, in := range cases {
		if out := Secrets(in); out != in {
			t.Errorf("text altered:\n in  = %q\n out = %q", in, out)
		}
	}
}

func TestSecretsMasksPEMPrivateKey(t *testing.T) {
	in := "before\n-----BEGIN OPENSSH PRIVATE KEY-----\nabc123\n-----END OPENSSH PRIVATE KEY-----\nafter"
	out := Secrets(in)
	if contains(out, "abc123") {
		t.Fatalf("PEM key body survived: %q", out)
	}
	if !contains(out, MaskToken) {
		t.Fatalf("redaction marker absent: %q", out)
	}
	if !contains(out, "before") || !contains(out, "after") {
		t.Fatalf("surrounding text altered: %q", out)
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
