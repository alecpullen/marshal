package settings

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/huh/v2"

	"marshal/internal/app/config"
)

type mcpServerEntry struct {
	key    string
	server config.MCPServerConfig
}

func (m mcpServerEntry) Title() string {
	return m.key + "  (" + m.server.Command + ")"
}
func (m mcpServerEntry) Key() string { return m.key }

func newMCPPane(s *state) sectionPane {
	form := newScalarPane(func() *huh.Form {
		b := &struct{ threshold string }{threshold: strconv.Itoa(s.cfg.MCP.DisclosureThresholdTools)}
		return newSectionForm(
			numField("Disclosure threshold tools", &b.threshold, 0, func(v int) {
				s.cfg.MCP.DisclosureThresholdTools = v
			}),
		)
	})
	serversSpec := collectionSpec{
		heading:   "Servers",
		keyPrompt: "New server name",
		entries: func(s *state) []collectionEntry {
			out := make([]collectionEntry, 0, len(s.cfg.MCP.Servers))
			for k, srv := range s.cfg.MCP.Servers {
				out = append(out, mcpServerEntry{key: k, server: srv})
			}
			return out
		},
		add: func(s *state, key string) error {
			if key == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.MCP.Servers[key]; ok {
				return fmt.Errorf("entry already exists")
			}
			s.cfg.MCP.Servers[key] = config.MCPServerConfig{Env: map[string]string{}}
			return nil
		},
		editForm: func(s *state, key string) (*huh.Form, func()) {
			local := s.cfg.MCP.Servers[key]
			argsBuf := strings.Join(local.Args, " ")
			envBuf := joinEnv(local.Env)
			form := newSectionForm(
				huh.NewInput().Key("Command").Title("Command").Value(&local.Command),
				huh.NewInput().Key("Args (space-separated)").Title("Args (space-separated)").Value(&argsBuf),
				huh.NewInput().Key("Env (KEY=VAL, comma-separated)").Title("Env (KEY=VAL, comma-separated)").Value(&envBuf),
			)
			return form, func() {
				local.Args = splitArgs(argsBuf)
				local.Env = splitEnv(envBuf)
				s.cfg.MCP.Servers[key] = local
			}
		},
		delete: func(s *state, key string) { delete(s.cfg.MCP.Servers, key) },
	}
	collection := newCollectionPane(s, serversSpec)
	policies := newMapEditor("Policies", &s.cfg.MCP.Policies)
	return newCompositePane(form, collection, policies)
}

func joinEnv(env map[string]string) string {
	parts := make([]string, 0, len(env))
	for k, v := range env {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func splitArgs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}

func splitEnv(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		if k, v, ok := strings.Cut(pair, "="); ok {
			out[k] = v
		}
	}
	return out
}
