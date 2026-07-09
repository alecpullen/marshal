package trust

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

func (r *TerminalResolver) Resolve(workingDir string, hasProjectConfig bool) (Decision, error) {
	abs, _ := filepath.Abs(workingDir)
	if !hasProjectConfig {
		return DecisionDontTrust, nil
	}
	trusted, err := r.store.IsTrusted(abs)
	if err != nil {
		return DecisionDontTrust, err
	}
	if trusted {
		return DecisionTrustPermanent, nil
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
	abs, _ := filepath.Abs(workingDir)
	return r.store.SetTrust(abs, decision == DecisionTrustPermanent, "")
}
