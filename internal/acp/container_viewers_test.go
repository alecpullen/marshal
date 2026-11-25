package acp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/web/bridge/contract"
)

// registeredHandlers builds an agent host with no runtime and registers every
// handler a connection carries on a fresh server, returning that server's
// handler table. TestACPClientMethodTableMatchesTheServer reads it for a stdio
// connection and TestTheContainerListNamesRegisteredMethods for a viewer's, so
// both check the registrations the same way.
func registeredHandlers(t *testing.T, viewer bool) map[string]Handler {
	t.Helper()
	h, err := newAgentHost(runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			return nil, errors.New("no runtime in tests")
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("newAgentHost: %v", err)
	}

	srv := NewServer(strings.NewReader(""), io.Discard)
	var alive atomic.Bool
	alive.Store(true)
	cleanup := h.registerHandlers(srv, &alive, h.sink.Notify, viewer)
	t.Cleanup(cleanup)
	return srv.handlers
}

// containerViewerOutsideTheTable are the four methods the client sends outside
// acpclient.Methods: the connection's own calls, which no Session or Host row
// carries.
var containerViewerOutsideTheTable = []string{
	"initialize",
	"session/stack_busy",
	"session/sync",
	acpwire.MethodCancelRequest,
}

// containerViewerRefusals are the session screen's methods decision 1 keeps off
// the container list: the lifecycle and the ending, credentials and sign-ins,
// and the settings' writes. Every method of these that acpclient.Methods names
// is here, so a new one of the screen fails
// TestEverySessionScreenMethodIsClassified until someone puts it on one side.
var containerViewerRefusals = []string{
	// The lifecycle and the ending: a viewer does not birth, end or close the
	// session it watches.
	"session/new",
	"session/close",
	"session/load",
	"session/resume",
	"session/delete",
	"project/hold",
	"project/release",
	"session/end_work",
	"session/end_accept",
	"session/end_wait",
	"session/exit_analysis",

	// Credentials and sign-ins: a sudo password is a credential entering the
	// container, and so is a provider or MCP sign-in.
	"session/answer_sudo",
	"session/set_sudo_unlock",
	"session/mcp_login",
	"session/mcp_reconnect",

	// The settings' writes: they change the host's configuration and its
	// installed skills and plugins, not the session's own state. The Studio
	// access surface is the same kind of authority — the bridge's stored
	// policy for this session — so a containerized viewer reads and writes
	// neither.
	"session/apply_config",
	"session/reload_config",
	"session/save_project_config",
	"session/save_verify_commands",
	"session/marshal_access",
	"session/save_marshal_access",
	"session/select_marshal_instance",
	"session/skills_install",
	"session/skills_remove",
	"session/skills_autoload",
	"session/skills_prefer_shared",
	"session/skill_gate_set",
	"session/skill_gate_clear",
	"session/plugins_install_scan",
	"session/plugins_install_confirm",
	"session/plugins_install_discard",
	"session/plugins_remove",

	// The connection's own cancel is a notification the server handles on its
	// read goroutine rather than through a handler, so it is no acpclient.Methods
	// Session row and TestEverySessionScreenMethodIsClassified cannot see it. It
	// is off the list, and a viewer's cancel is dropped by omission; naming it
	// here keeps that a decision rather than an accident.
	"session/cancel",
}

// TestTheContainerListIsTheSessionScreens pins every entry of the list to a
// Session row of acpclient.Methods, or to one of the four methods the client
// sends outside that table: the list may not name a method of its own.
func TestTheContainerListIsTheSessionScreens(t *testing.T) {
	allowed := make(map[string]bool, len(acpclient.Methods)+len(containerViewerOutsideTheTable))
	for _, row := range acpclient.Methods {
		if row.Iface == "Session" && row.ACP != "" {
			allowed[row.ACP] = true
		}
	}
	for _, method := range containerViewerOutsideTheTable {
		allowed[method] = true
	}

	for _, method := range contract.ContainerViewerMethods {
		if !allowed[method] {
			t.Errorf("container list holds %s, which no Session row of acpclient.Methods carries and is no connection call outside the table", method)
		}
	}
}

// TestTheContainerListHoldsNoHostCallOrWrite keeps the list off the host's
// side of the seam: no Host method's ACP call, nothing under config/ or trust/,
// and none of decision 1's named refusals.
func TestTheContainerListHoldsNoHostCallOrWrite(t *testing.T) {
	host := make(map[string]bool, len(acpclient.Methods))
	for _, row := range acpclient.Methods {
		if row.Iface == "Host" && row.ACP != "" {
			host[row.ACP] = true
		}
	}
	refused := make(map[string]bool, len(containerViewerRefusals))
	for _, method := range containerViewerRefusals {
		refused[method] = true
	}

	for _, method := range contract.ContainerViewerMethods {
		switch {
		case host[method]:
			t.Errorf("container list holds %s, a Host method: a container does not reach the host's side of the seam", method)
		case strings.HasPrefix(method, "config/"):
			t.Errorf("container list holds %s: a container writes no configuration", method)
		case strings.HasPrefix(method, "trust/"):
			t.Errorf("container list holds %s: a container decides no trust", method)
		case refused[method]:
			t.Errorf("container list holds %s, which decision 1 refuses", method)
		}
	}
}

// TestEverySessionScreenMethodIsClassified makes the two sides exhaustive: a
// Session row's ACP method is on the list or among the named refusals, so a new
// one of the screen fails here until it is placed.
func TestEverySessionScreenMethodIsClassified(t *testing.T) {
	listed := make(map[string]bool, len(contract.ContainerViewerMethods))
	for _, method := range contract.ContainerViewerMethods {
		listed[method] = true
	}
	refused := make(map[string]bool, len(containerViewerRefusals))
	for _, method := range containerViewerRefusals {
		refused[method] = true
	}

	for _, row := range acpclient.Methods {
		if row.Iface != "Session" || row.ACP == "" {
			continue
		}
		if !listed[row.ACP] && !refused[row.ACP] {
			t.Errorf("acpclient.Methods' Session.%s carries %s, which the container list neither holds nor refuses: place it", row.Name, row.ACP)
		}
	}
}

// TestTheContainerListNamesRegisteredMethods keeps the list to methods a viewer
// connection serves. $/cancel_request is the exception: the server handles the
// protocol's own cancel on its read goroutine, not through a handler.
func TestTheContainerListNamesRegisteredMethods(t *testing.T) {
	handlers := registeredHandlers(t, true)
	for _, method := range contract.ContainerViewerMethods {
		if method == acpwire.MethodCancelRequest {
			continue
		}
		if _, ok := handlers[method]; !ok {
			t.Errorf("container list holds %s, which a viewer connection registers no handler for", method)
		}
	}
}

// TestTheContainerListIsSorted keeps the list's order checkable against its
// written form.
func TestTheContainerListIsSorted(t *testing.T) {
	if !sort.StringsAreSorted(contract.ContainerViewerMethods) {
		t.Errorf("ContainerViewerMethods is not sorted: %v", contract.ContainerViewerMethods)
	}
}

// TestTheBridgeNamesTheViewersVariable pins the one literal the boundary forces
// the bridge to write out: web/ is standard library only, so
// web/bridge/container.go may not import this package and carries the
// variable's name itself. A rename here must fail here rather than leave every
// container's agent never told where to serve viewers.
func TestTheBridgeNamesTheViewersVariable(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "web", "bridge", "container.go"))
	if err != nil {
		t.Fatalf("read the bridge's container.go: %v", err)
	}
	literal := `containerViewersEnv = "` + ViewersEnv + `"`
	if !strings.Contains(string(source), literal) {
		t.Fatalf("web/bridge/container.go does not carry %s: a container's agent would never learn where to serve viewers", literal)
	}
}
