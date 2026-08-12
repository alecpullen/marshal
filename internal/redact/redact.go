// Package redact masks secret-bearing values in rendered text for safe export
// (docs/12 F15 R2). The config flag privacy.redact_secrets gates it. This is
// a content scanner; the sandbox's env-key scrubber (internal/sandbox) is a
// separate, narrower mechanism for exec env. Key-name checks now delegate to
// envutil.IsSecretKey so the export redactor agrees with the sandbox.
package redact

import (
	"regexp"
	"strings"

	"marshal/internal/sandbox/envutil"
)

// MaskToken is the placeholder substituted for any redacted value.
const MaskToken = "[REDACTED]"

// secretAssignment matches `KEY=value` or `KEY: value`. The key is checked
// against envutil.IsSecretKey so the export redactor agrees with the sandbox
// on which key names are secret-bearing.
var secretAssignment = regexp.MustCompile(`(?i)\b([A-Za-z0-9_]+)\s*([:=])\s*([^\s]+)`)

// highEntropyToken matches common token sigils so bare tokens in logs/prompts
// are masked even when no KEY= form is present. Assembled from named parts
// rather than one long literal so a missing vendor is easy to spot.
//
// Only credentials belong here. Stripe's pk_live_ publishable keys are
// deliberately absent: they are designed to ship in client code, and masking
// them would corrupt legitimate transcripts for no gain.
var highEntropyToken = regexp.MustCompile(`(?i)\b(` + strings.Join([]string{
	// OpenAI / Anthropic (covers sk-ant-, sk-proj-).
	`sk-[A-Za-z0-9_-]{16,}`,
	// GitHub personal, OAuth, user-to-server, server-to-server, fine-grained.
	`ghp_[A-Za-z0-9]{20,}`,
	`gho_[A-Za-z0-9]{20,}`,
	`ghu_[A-Za-z0-9]{20,}`,
	`ghs_[A-Za-z0-9]{20,}`,
	`github_pat_[A-Za-z0-9_]{20,}`,
	// AWS access key ID.
	`AKIA[0-9A-Z]{12,}`,
	// GitLab personal access token.
	`glpat-[A-Za-z0-9_-]{16,}`,
	// Slack bot/user/app/refresh/legacy tokens.
	`xox[baprs]-[A-Za-z0-9-]{10,}`,
	// Stripe secret and restricted keys, and webhook signing secrets. The
	// separator is an underscore, so the sk- branch above never matched
	// these.
	`[sr]k_(?:live|test)_[A-Za-z0-9]{16,}`,
	`whsec_[A-Za-z0-9]{16,}`,
	// Google API keys: the AIza sigil plus 35 payload characters. Bounded
	// below but not above — a strict {35} combined with the trailing \b
	// would match nothing at all on a longer payload, silently leaking the
	// very thing this pattern exists to catch.
	`AIza[A-Za-z0-9_-]{35,}`,
}, "|") + `)\b`)

// bearerJWT matches `Bearer <jwt>` authorization headers.
var bearerJWT = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)

// privateKeyBlock matches PEM-encoded private-key blocks (including RSA, EC,
// DSA, and OpenSSH variants). The (?s) flag lets `.` match newlines so the
// entire block is replaced.
var privateKeyBlock = regexp.MustCompile(`(?is)-----BEGIN(?:\s+(?:RSA|EC|DSA|OPENSSH))? PRIVATE KEY-----.*?-----END(?:\s+(?:RSA|EC|DSA|OPENSSH))? PRIVATE KEY-----`)

// Secrets returns text with secret-bearing values replaced by MaskToken. It is
// conservative: it only masks values adjacent to known bearer keys/sigils, so
// ordinary prose and code are untouched.
func Secrets(text string) string {
	text = secretAssignment.ReplaceAllStringFunc(text, func(match string) string {
		subs := secretAssignment.FindStringSubmatch(match)
		if len(subs) < 4 {
			return match
		}
		key, op, val := subs[1], subs[2], subs[3]
		if envutil.IsSecretKey(key) {
			return key + op + MaskToken
		}
		// The key itself isn't secret, but the value may contain a secret
		// assignment (e.g. "error: DB_PASSWORD=hunter2"). Recursively redact
		// just the value so nested secrets are still masked.
		redactedVal := Secrets(val)
		if redactedVal != val {
			return key + op + redactedVal
		}
		return match
	})
	text = highEntropyToken.ReplaceAllString(text, MaskToken)
	text = bearerJWT.ReplaceAllString(text, MaskToken)
	text = privateKeyBlock.ReplaceAllString(text, MaskToken)
	return text
}
