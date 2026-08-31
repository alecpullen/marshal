package trust

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
)

type TerminalResolver struct {
	store   *Store
	session map[string]bool
	in      *bufio.Reader
	out     io.Writer
}

func NewTerminalResolver(store *Store) *TerminalResolver {
	return &TerminalResolver{
		store:   store,
		session: map[string]bool{},
		in:      bufio.NewReader(os.Stdin),
		out:     os.Stdout,
	}
}

// SetIn overrides the stdin reader used by the trust prompt. Test-only.
func (r *TerminalResolver) SetIn(in io.Reader) {
	r.in = bufio.NewReader(in)
}

// SetOut overrides the stdout writer used by the trust prompt. Test-only.
func (r *TerminalResolver) SetOut(out io.Writer) {
	r.out = out
}

func (r *TerminalResolver) Resolve(workingDir string, hasProjectConfig bool) (Decision, error) {
	abs := Canonicalize(workingDir)
	if !hasProjectConfig {
		return DecisionDontTrust, nil
	}
	trusted, err := r.store.IsTrusted(abs)
	if err != nil {
		return DecisionDontTrust, err
	}
	if trusted {
		// Re-validate the stored trust against the current on-disk
		// project config. The trust record stores a hash of the config
		// that was trusted; if the config has changed since (e.g. a
		// malicious commit added [[hooks.entries]]), we must re-prompt
		// rather than silently extend trust to the new content.
		currentHash, hashErr := ConfigHashFor(workingDir)
		if hashErr != nil {
			return DecisionDontTrust, hashErr
		}
		storedHash, _ := r.store.StoredConfigHash(abs)
		if storedHash == currentHash {
			return DecisionTrustPermanent, nil
		}
		// Config changed since trust was recorded: fall through to the
		// trust prompt as if the project were newly seen.
	}
	if r.session[abs] {
		return DecisionTrustSession, nil
	}
	if !term.IsTerminal(os.Stdin.Fd()) {
		return DecisionDontTrust, nil
	}
	// Prompt.
	fmt.Fprintf(r.out, "\nThis project has a .marshal/config.toml.\n")
	fmt.Fprintf(r.out, "It can change providers, policy rules, and commands.\n\n")
	fmt.Fprintf(r.out, "  1) Trust permanently (saved for this path)\n")
	fmt.Fprintf(r.out, "  2) Trust this session only\n")
	fmt.Fprintf(r.out, "  3) Don't trust (user + default config only)\n\n")
	fmt.Fprintf(r.out, "Choose [1-3]: ")
	line, err := r.in.ReadString('\n')
	if err != nil {
		return DecisionDontTrust, err
	}
	switch strings.TrimSpace(line) {
	case "1":
		r.session[abs] = true
		return DecisionTrustPermanent, nil
	case "2":
		r.session[abs] = true
		return DecisionTrustSession, nil
	default:
		return DecisionDontTrust, nil
	}
}

func (r *TerminalResolver) Record(workingDir string, decision Decision) error {
	abs := Canonicalize(workingDir)
	// Persist the current config hash alongside the trust decision so
	// future Resolve calls can detect config changes and re-prompt.
	// Hash errors fall back to empty string (treated as "no hash" by
	// Resolve, which still re-prompts because the stored hash will
	// not match the current one).
	configHash, _ := ConfigHashFor(workingDir)
	return r.store.SetTrust(abs, decision == DecisionTrustPermanent, configHash)
}
