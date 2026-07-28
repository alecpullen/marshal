package settings

import (
	"fmt"

	"marshal/internal/app/config"
)

func mcpFrame(s *state) *frame {
	serversDrill := entriesDrill("mcp.servers", "Servers", "New server name",
		func() []string { return sortedKeys(s.cfg.MCP.Servers) },
		func(k string) string { return k + "  (" + s.cfg.MCP.Servers[k].Command + ")" },
		func(k string) error {
			if k == "" {
				return fmt.Errorf("name cannot be empty")
			}
			if _, ok := s.cfg.MCP.Servers[k]; ok {
				return fmt.Errorf("entry already exists")
			}
			if s.cfg.MCP.Servers == nil {
				s.cfg.MCP.Servers = map[string]config.MCPServerConfig{}
			}
			s.cfg.MCP.Servers[k] = config.MCPServerConfig{Env: map[string]string{}}
			return nil
		},
		func(k string) *frame {
			// Args and Env need stable pointers for the drill builders; the
			// writeback closure writes the struct back after each change. We keep
			// a per-frame copy whose slices/maps alias the working config
			// only through explicit writeback.
			return newFrame(k, func() []*field {
				srv := s.cfg.MCP.Servers[k]
				writeback := func() { s.cfg.MCP.Servers[k] = srv }
				cmdField := scalarField("mcp.servers."+k+".command", "Command",
					func() string { return s.cfg.MCP.Servers[k].Command },
					func(v string) error {
						srv.Command = v
						writeback()
						return nil
					})
				cmdField.Desc = "MCP server command to execute"
				return []*field{
					cmdField,
					// Args/Env drills operate on fresh copies bound through
					// closures that write back on every mutation.
					mcpArgsDrill(s, k),
					mcpEnvDrill(s, k),
				}
			})
		},
		func(k string) { delete(s.cfg.MCP.Servers, k) })

	return newFrame("MCP", func() []*field {
		dtField := intField("mcp.disclosure_threshold", "Disclosure threshold tools",
			func() int { return s.cfg.MCP.DisclosureThresholdTools }, 0,
			func(v int) { s.cfg.MCP.DisclosureThresholdTools = v })
		dtField.Desc = "max tools before MCP capabilities are disclosed"
		return []*field{
			dtField,
			serversDrill,
			mapStringDrill("mcp.policies", "Policies", &s.cfg.MCP.Policies),
		}
	})
}

// mcpArgsDrill is listDrill's shape, but map-stored structs can't hand out
// a *[]string into the map value — every mutation must write the struct
// back. It reimplements the small list frame against getter/setter closures.
func mcpArgsDrill(s *state, server string) *field {
	get := func() []string { return s.cfg.MCP.Servers[server].Args }
	set := func(args []string) {
		srv := s.cfg.MCP.Servers[server]
		srv.Args = args
		s.cfg.MCP.Servers[server] = srv
	}
	buildFields := func() []*field {
		args := get()
		out := make([]*field, len(args))
		for i := range args {
			i := i
			out[i] = &field{
				ID: fmt.Sprintf("mcp.servers.%s.args.%d", server, i), Title: args[i], Kind: kindScalar,
				Desc:   "MCP server command-line argument",
				GetStr: func() string { return get()[i] },
				SetStr: func(v string) error {
					if v == "" {
						return fmt.Errorf("cannot be empty")
					}
					a := get()
					a[i] = v
					set(a)
					return nil
				},
				Del: func() {
					a := get()
					set(append(a[:i], a[i+1:]...))
				},
			}
		}
		return out
	}
	return &field{
		ID: "mcp.servers." + server + ".args", Title: "Args", Kind: kindDrill,
		Summary: func() string { return fmt.Sprintf("%d items", len(get())) },
		Build: func() *frame {
			return newCollectionFrame("Args", "New entry", buildFields, func(v string) error {
				if v == "" {
					return fmt.Errorf("cannot be empty")
				}
				set(append(get(), v))
				return nil
			})
		},
	}
}

// mcpEnvDrill: Env maps inside the server struct are reference types, so
// mutating the map through the struct copy mutates the stored map directly;
// only nil-map creation needs a writeback.
func mcpEnvDrill(s *state, server string) *field {
	ensure := func() map[string]string {
		srv := s.cfg.MCP.Servers[server]
		if srv.Env == nil {
			srv.Env = map[string]string{}
			s.cfg.MCP.Servers[server] = srv
		}
		return srv.Env
	}
	buildFields := func() []*field {
		env := ensure()
		keys := sortedKeys(env)
		out := make([]*field, len(keys))
		for i, k := range keys {
			k := k
			out[i] = &field{
				ID: "mcp.servers." + server + ".env." + k, Title: k, Kind: kindScalar,
				Desc:   "MCP server environment variable",
				GetStr: func() string { return ensure()[k] },
				SetStr: func(v string) error { ensure()[k] = v; return nil },
				Del:    func() { delete(ensure(), k) },
			}
		}
		return out
	}
	return &field{
		ID: "mcp.servers." + server + ".env", Title: "Env", Kind: kindDrill,
		Summary: func() string { return fmt.Sprintf("%d entries", len(ensure())) },
		Build: func() *frame {
			return newCollectionFrame("Env", "New key", buildFields, func(k string) error {
				if k == "" {
					return fmt.Errorf("key cannot be empty")
				}
				env := ensure()
				if _, ok := env[k]; ok {
					return fmt.Errorf("key already exists")
				}
				env[k] = ""
				return nil
			})
		},
	}
}
