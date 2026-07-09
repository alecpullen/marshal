// Package redact masks secret-bearing values in rendered text for safe export
// (docs/12 F15 R2). The config flag privacy.redact_secrets gates it. This is
// a content scanner; the sandbox's env-key scrubber (internal/sandbox) is a
// separate, narrower mechanism for exec env. The key prefixes here mirror the
// sandbox's isSecretBearer list so the two agree on what counts as a secret.
package redact

import "regexp"

// MaskToken is the placeholder substituted for any redacted value.
const MaskToken = "[REDACTED]"

// secretAssignment matches `KEY=value` or `KEY: value` for known secret-bearing
// key prefixes, capturing the value to mask. Values are masked up to the next
// whitespace or end-of-line.
var secretAssignment = regexp.MustCompile(
	`(?i)(AWS_SECRET_ACCESS_KEY|AWS_SESSION_TOKEN|AWS_ACCESS_KEY_ID|OPENAI_API_KEY|ANTHROPIC_API_KEY|OPENROUTER_API_KEY|GITHUB_TOKEN|HUGGINGFACE_TOKEN|COHERE_API_KEY|GOOGLE_API_KEY)\s*[:=]\s*([^\s]+)`,
)

// highEntropyToken matches common token sigils (sk-..., ghp_..., AKIA...,
// glpat-...) so bare tokens in logs/prompts are masked even when no KEY= form
// is present.
var highEntropyToken = regexp.MustCompile(`(?i)\b(sk-[A-Za-z0-9_-]{16,}|ghp_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{12,}|glpat-[A-Za-z0-9_-]{16,})\b`)

// Secrets returns text with secret-bearing values replaced by MaskToken. It is
// conservative: it only masks values adjacent to known bearer keys/sigils, so
// ordinary prose and code are untouched.
func Secrets(text string) string {
	text = secretAssignment.ReplaceAllString(text, "${1}="+MaskToken)
	text = highEntropyToken.ReplaceAllString(text, MaskToken)
	return text
}
