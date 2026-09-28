//go:build probe_c

package main

import (
	"fmt"
	"os"
)

// probe_codex is the OAuth Codex spike probe
// (design: .docs-archive/superpowers/specs/2026-09-14-oauth-chatgpt-providers-design.md).
// Run with: go run -tags probe_c ./cmd/marshal/probe_codex <subcommand> [flags]
// Subcommands: auth-begin, auth-poll, decode-token, test-chat, models, quota, caps, prompt-enforcement
func main() { os.Exit(probeCodexMain()) }

func probeCodexMain() int {
	if len(os.Args) < 2 {
		probeCodexUsage()
		return 2
	}
	sub := os.Args[1]
	args := os.Args[2:]
	var err error
	switch sub {
	case "auth-begin":
		err = probeAuthBegin(args)
	case "auth-poll":
		err = probeAuthPoll(args)
	case "decode-token":
		err = probeDecodeToken()
	case "test-chat":
		err = probeTestChat(args)
	case "models":
		err = probeModels()
	case "quota":
		err = probeQuota()
	case "caps":
		err = probeCaps(args)
	case "prompt-enforcement":
		err = probePromptEnforcement()
	default:
		fmt.Fprintf(os.Stderr, "unknown probe subcommand %q\n", sub)
		probeCodexUsage()
		return 2
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe %s: %v\n", sub, err)
		return 1
	}
	return 0
}

func probeCodexUsage() {
	fmt.Fprintln(os.Stderr, `usage: go run -tags probe_c ./cmd/marshal/probe_codex <subcommand>

  auth-begin          start OAuth flow (prints URL/code), save state to ~/.cache/marshal-codex-spike/
  auth-poll           complete flow, exchange code, store tokens
  decode-token        decode stored JWT: claims, account id, expiry
  test-chat           minimal Responses call against codex backend
  models              probe model listing endpoints
  quota               read quota/rate-limit headers
  caps                per-model capability probe (tool calling, structured output) [-model name]
  prompt-enforcement  test whether endpoint enforces its own system prompt`)
}
