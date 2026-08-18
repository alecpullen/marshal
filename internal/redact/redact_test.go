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
	var (
		stripeSecret     = synthSecret("sk"+"_live_", 24)
		stripeTest       = synthSecret("sk"+"_test_", 24)
		stripeRestricted = synthSecret("rk"+"_live_", 24)
		stripeWebhook    = synthSecret("whsec"+"_", 24)
		// 35 payload characters: the real Google API key length.
		googleKey = synthSecret("AI"+"za", 35)
		// One character longer, to prove a longer payload still masks.
		googleKeyOverlong = synthSecret("AI"+"za", 36)
		slackUser         = synthSecret("xox"+"p-", 20)
		slackApp          = synthSecret("xox"+"a-", 20)
	)
	cases := []struct {
		name   string
		input  string
		secret string
	}{
		{"stripe secret", "charge failed for " + stripeSecret, stripeSecret},
		{"stripe test", "using " + stripeTest + " here", stripeTest},
		{"stripe restricted", "using " + stripeRestricted + " here", stripeRestricted},
		{"stripe webhook", "signing with " + stripeWebhook, stripeWebhook},
		{"google api key", "maps call used " + googleKey, googleKey},
		{"google api key overlong", "key " + googleKeyOverlong + " here", googleKeyOverlong},
		{"slack user", "token is " + slackUser, slackUser},
		{"slack app", "token is " + slackApp, slackApp},
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
		"the publishable key " + synthSecret("pk"+"_live_", 24) + " is fine to commit",
		// Too short to be a Google key (35 payload chars required).
		"identifier " + synthSecret("AI"+"za", 8) + " is not a credential",
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

func TestSecretsHandlesSpacesInSecrets(t *testing.T) {
	// A secret value containing spaces should be fully redacted, not
	// partially redacted by splitting on whitespace (which would leave the
	// remainder of the value — e.g. "secret key here" — visible).
	in := "API_KEY=my secret key here"
	out := Secrets(in)
	if out != "API_KEY="+MaskToken {
		t.Fatalf("secret with spaces not fully redacted: %q", out)
	}
	if contains(out, "secret") || contains(out, "key") {
		t.Fatalf("secret remainder survived redaction: %q", out)
	}
}

// TestSecretsDoesNotSwallowTrailingProse guards against over-redaction: the
// value capture must be bounded so that prose or subsequent KEY=value pairs
// on the same line survive. A naive "rest of line" capture would swallow
// everything after the first secret.
func TestSecretsDoesNotSwallowTrailingProse(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		kept   string
		secret string
	}{
		// A multi-word secret is fully redacted, but prose beyond the
		// bounded value survives.
		{"trailing prose", "API_KEY=my secret key here and the deploy finished", "and the deploy finished", "my secret key here"},
		// An adjacent secret pair is redacted, not swallowed into the first
		// value; the second key survives.
		{"adjacent pair", "API_KEY=foo GITHUB_TOKEN=bar", "GITHUB_TOKEN=", "foo"},
		// A single-word token followed by more than the bounded number of
		// prose words keeps the overflow prose.
		{"overflow prose", "GITHUB_TOKEN=ghp_abcdef1234567890 then the build passed successfully", "successfully", "ghp_abcdef1234567890"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Secrets(tc.input)
			if contains(out, tc.secret) {
				t.Fatalf("secret %q survived redaction: %q", tc.secret, out)
			}
			if !contains(out, tc.kept) {
				t.Fatalf("trailing content %q was swallowed: %q", tc.kept, out)
			}
		})
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

// synthSecret builds a credential-shaped test fixture at runtime: a sigil
// followed by a generated alphanumeric payload of the given length.
//
// The sigils are deliberately written as split literals ("sk" + "_live_") and
// the payload is generated rather than typed. Realistic-looking fixtures are
// indistinguishable from live credentials to a secret scanner — Betterleaks
// flagged the previous inline literals as high-severity Stripe, GCP and Slack
// leaks — and a test file full of scanner alerts trains reviewers to wave
// those alerts through. Please don't "simplify" these back into contiguous
// literals.
//
// The payload alphabet is alphanumeric only, so it satisfies the token
// patterns in redact.go and keeps the \b boundaries at each end intact.
func synthSecret(sigil string, payloadLen int) string {
	const alphabet = "ABCdefGHIjkLMNopqRSTuvwxYZ0123456789"
	payload := make([]byte, payloadLen)
	for i := range payload {
		payload[i] = alphabet[i%len(alphabet)]
	}
	return sigil + string(payload)
}
