package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"marshal/web/bridge/contract"
)

// maxBodyBytes bounds inbound request bodies. Prompt and steer text dominate;
// the bound is also above the largest planner body, a plan and a spec at their
// per-document limits.
const maxBodyBytes = 2 << 20

// maxAuditTail caps how many audit records a single feed request may
// return. The UI polls every few seconds; a bounded tail keeps the
// response small and the log readable.
const maxAuditTail = 200

// Server exposes the Registry and EventLog over HTTP: a JSON REST API
// under /api plus the SSE event stream. It holds no token: the listener it
// serves on is reached by the user who started it, and what arrives there
// was authenticated by the operator's proxy (webguard.go).
type Server struct {
	fleet *Fleet
	mux   *http.ServeMux
	reg   *Registry
	log   *EventLog
	// publicURLBase is the externally reachable base URL, used to build
	// absolute links (e.g. the operator-approval URL an MCP spawn
	// returns). Empty falls back to the listen address.
	publicURLBase string
	// previewPort is the port of the separate preview listener, 0 until
	// the host has started one (see SetPreviewPort).
	previewPort atomic.Int32
	// previewAddr is the preview listener's bound address (host:port), nil
	// until the host has started one. GET /api/daemon reports it (see
	// SetPreviewAddr).
	previewAddr atomic.Pointer[string]
	// previewURL, when set, is the public origin issued preview links
	// name in place of the request's host and the preview port (see
	// SetPreviewURL).
	previewURL string
	// daemonInfo, when set, is what GET /api/daemon answers. Nil on a
	// server that is not a daemon, which answers 404 (decision 8).
	daemonInfo *DaemonInfo
	// studioDir is the directory the Studio is served from, and
	// studioLooked the candidates that were searched for it, in order.
	// An empty studioDir answers the no-files page naming studioLooked
	// (SetStudioDir, one-daemon D12).
	studioDir    string
	studioLooked []string
	// refresh, when set, is the daemon's refresh helper, reported per
	// request by GET /api/daemon (see SetRefreshHelper).
	refresh *Helper
	// hostsMu guards hosts, the last report the tunnel helper made
	// (daemonhosts.go).
	hostsMu sync.Mutex
	// hosts is the tunnel helper's last report, sorted by name and never
	// nil once reported: GET /api/daemon answers it.
	hosts []DaemonHost
	// fleetCmd runs a host edit on this machine (hostedit.go). Nil runs the
	// fleet's own marshal binary.
	fleetCmd fleetCommand
	// hostsEdits is one fleet.toml edit at a time: adding and removing a host
	// both write the one file, so they never overlap (one-daemon D15).
	hostsEdits sync.Mutex
	// hostTransports is one HTTP transport per host socket path: a forward to
	// a host dials its tunnel socket through the transport built for it, and
	// keeps it, so a stream keeps its connection (hostforward.go).
	hostTransports sync.Map
	// relaysMu guards both relay tables below (previewrelay.go). A forward
	// records a link a host issued, and a request on this daemon's preview
	// listener reads it back.
	relaysMu sync.Mutex
	// relaysByToken is the forwarded previews this daemon may relay, by the
	// token the link carries.
	relaysByToken map[string]previewRelay
	// relaysByAgent is the same table keyed by agent ID and port, "<id>/<port>",
	// for a link whose credential is a ?t= or a cookie.
	relaysByAgent map[string]previewRelay
	// factoryAuth is the one factory policy lock: grant management, client
	// deletion, project removal, delegation settings and the authorization
	// guarding a durable factory mutation all take it. It is created with the
	// server, over the workspace it reads, so every route that can change
	// authority or act under it shares one mutex.
	factoryAuth *factoryMCPAuthorizer
	// factory is the same factory service the Studio routes serve, kept here
	// so the MCP tools compose the identical instance. It is nil when this
	// server was built without one, and a factory tool then answers
	// unavailable instead of taking an unauthorised path.
	factory FactoryAPI
	// marshalEnroll is the record of live built-in enrollments: one binding per
	// enrolled session, in memory only, so a restart ends every one of them
	// (marshal_enrollment.go).
	marshalEnroll *marshalEnrollments
	// coordinator is Marshal's own host-local destination: the durable tasks and
	// the attention read over them (coordinator.go). It is nil on a server with
	// no fleet state directory, and its routes then answer unavailable.
	coordinator *coordinatorService
}

// NewServer builds a server over a *Fleet or a *Registry. For a fleet, the
// first string after it is the public URL. For a registry, the *EventLog
// comes first and the public URL second. A FactoryAPI anywhere in args is
// the factory.
func NewServer(target any, args ...any) *Server {
	var fleet *Fleet
	var reg *Registry
	var log *EventLog
	var publicURLBase string
	if f, ok := target.(*Fleet); ok {
		fleet = f
		if len(args) > 0 {
			publicURLBase, _ = args[0].(string)
		}
	} else if r, ok := target.(*Registry); ok {
		reg = r
		if len(args) > 0 {
			log, _ = args[0].(*EventLog)
		}
		if len(args) > 1 {
			publicURLBase, _ = args[1].(string)
		}
	}
	if fleet != nil && publicURLBase != "" {
		fleet.SetPublicURLBase(publicURLBase)
	}
	var ws *Workspace
	if fleet != nil {
		ws = fleet.ws
	}
	s := &Server{fleet: fleet, reg: reg, log: log, publicURLBase: publicURLBase, mux: http.NewServeMux(),
		factoryAuth:   newFactoryMCPAuthorizer(ws),
		marshalEnroll: newMarshalEnrollments(),
		relaysByToken: make(map[string]previewRelay),
		relaysByAgent: make(map[string]previewRelay)}
	s.routes()
	// The planner API owns live backend sessions, so it exists only when this
	// server has a fleet to hold them.
	if fleet != nil {
		s.plannerRoutes()
		// Marshal's own destination is host-local state: it exists when this
		// server has the fleet's state directory to keep it in.
		s.coordinatorSetup(fleet)
	}
	for _, arg := range args {
		if factory, ok := arg.(FactoryAPI); ok {
			s.factory = factory
			s.factoryRoutes(factory)
			// Working drafts live beside the factory journal, in a store of
			// their own. Only a factory API with a fleet state directory can
			// host one; anything else keeps its routes as they were.
			s.workingDraftSetup(factory)
		}
	}
	return s
}

// publicURL builds an absolute URL from the configured public URL or
// falls back to the listen address.
func (s *Server) publicURL(path string) string {
	base := s.publicURLBase
	if base == "" {
		base = "http://localhost:7700"
	}
	return strings.TrimRight(base, "/") + path
}

// ServeHTTP implements http.Handler: it is the web listener's handler.
// Non-API paths are served by the Studio's files (index.html fallback for
// client-side routing); /api paths go through the mux so method mismatches
// still produce 405. It has no bearer check — what arrives here came through
// the operator's proxy, which authenticated the caller (webguard.go) — and
// the socket-only routes are absent because they are not on the mux (D10,
// D13).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// What arrives here came through the operator's proxy, which authenticated
	// the browser but says nothing about who asked: a request addressed to
	// another host, one another site's page made, and an /api/ body that is
	// not JSON are refused before any route sees them (webguard.go, D13).
	if status, reason, ok := s.webGuard(r); !ok {
		writeJSON(w, status, map[string]string{"error": reason})
		return
	}
	s.route(s.mux, w, r)
}

// socketOnly reports whether path is a route only the socket serves: the
// project attach, the agent attach, pin, restart, the hosts report and
// enrollment. A request a host forwards from the network crosses to a tunnel
// socket, and these stay the owner's own operations there too (D10).
func socketOnly(path string) bool {
	switch path {
	case attachPath, pinPath, daemonRestartPath, daemonHostsPath, marshalEnrollPath:
		return true
	}
	_, ok := agentAttachID(path)
	return ok
}

// SocketHandler is the server's handler for the local socket: ServeHTTP's
// routing plus the routes only the socket serves. /mcp keeps its own
// per-client auth, so it reaches the bare mux either way.
//
// The project attach and the pin route exist only here: the network
// listener never opens a project's child and never changes what the daemon
// keeps running, because they are not on the mux (P4 D3, D9).
func (s *Server) SocketHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == attachPath {
			s.attachProject(w, r)
			return
		}
		if id, ok := agentAttachID(r.URL.Path); ok {
			s.attachAgent(w, r, id)
			return
		}
		if r.URL.Path == pinPath {
			s.pinProject(w, r)
			return
		}
		if r.URL.Path == daemonRestartPath {
			// Restarting the daemon ends every viewer and re-executes the
			// bridge, so it is the socket's own operation: the network
			// listener answers it 404 (P6 B5).
			s.restartDaemon(w, r)
			return
		}
		if r.URL.Path == daemonHostsPath {
			// The tunnel helper's report is the socket's own: the network
			// listener answers it 404 (one-daemon D7).
			s.reportHosts(w, r)
			return
		}
		if r.URL.Path == marshalEnrollPath {
			// Enrollment is the socket's own operation: it is reached only
			// where the owner is the caller, never on the network listener,
			// which answers it 404 (marshal_enrollment.go).
			s.enrollMarshal(w, r)
			return
		}
		s.route(s.mux, w, r)
	})
}

// route is ServeHTTP's routing with api in place of the mux: the web
// listener passes the bare mux, and the local socket passes it too, after
// the routes only a socket serves.
func (s *Server) route(api http.Handler, w http.ResponseWriter, r *http.Request) {
	// /mcp joins /api/ on the mux side. It is not under /api/ because it
	// authenticates per client rather than as the listener, but it must
	// still reach the mux rather than the SPA fallback.
	// Previews are served from their own origin (PreviewHandler), never
	// from this one: an app the agent runs must not share storage with the
	// UI.
	if strings.HasPrefix(r.URL.Path, statusPrefix) {
		s.statusPublic(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, hooksPrefix) {
		s.hooksPublic(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, previewPrefix) {
		http.NotFound(w, r)
		return
	}
	if isNextPath(r.URL.Path) {
		// The retired prefix: the Flutter Studio holds `/` now, so an old
		// /next/ link moves to the place it names (F8 cutover).
		nextRedirect(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/mcp" {
		// Everything else that is not the API is the Flutter Studio, at the
		// root. It is served from the directory the daemon found, read on
		// each request, or from the no-files page when it found none.
		s.studioHandler().ServeHTTP(w, r)
		return
	}
	api.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/daemon", s.daemon)
	// The hosts this daemon keeps a tunnel to (one-daemon D10): the list the
	// tunnel helper last reported, and the forward to each host's socket. Both
	// are /api/ routes, so both listeners serve them.
	s.mux.HandleFunc("GET "+hostsPath, s.listHosts)
	// Adding and removing a host run the CLI on this machine, which owns the
	// fleet.toml write (one-daemon D15, plan 7). The name pattern is one
	// segment, so a longer path is still the forward below.
	s.mux.HandleFunc("POST "+hostsPath, s.addHost)
	s.mux.HandleFunc("DELETE "+hostForwardPrefix+"{name}", s.removeHost)
	s.mux.Handle(hostForwardPrefix, http.HandlerFunc(s.hostForward))
	s.mux.HandleFunc("GET /api/config", s.config)
	s.mux.HandleFunc("GET /api/projects", s.listProjects)
	// A project's image upload: the ref it answers is what a prompt attaches.
	s.mux.HandleFunc("POST /api/projects/images", s.uploadImage)
	s.mux.HandleFunc("POST /api/projects", s.addProject)
	s.mux.HandleFunc("DELETE /api/projects", s.removeProject)
	s.mux.HandleFunc("GET /api/agents", s.listAgents)
	s.mux.HandleFunc("GET /api/inbox", s.inbox)
	s.mux.HandleFunc("POST /api/agents", s.spawnAgent)
	s.mux.HandleFunc("GET /api/agents/{id}/diff", s.agentDiff)
	s.mux.HandleFunc("GET /api/agents/{id}/files", s.agentFiles)
	s.mux.HandleFunc("GET /api/agents/{id}/file", s.agentFile)
	s.mux.HandleFunc("GET /api/agents/{id}/commit-draft", s.agentCommitDraft)
	s.mux.HandleFunc("POST /api/agents/{id}/verify", s.agentVerify)
	s.mux.HandleFunc("GET /api/agents/{id}/gate", s.agentGate)
	s.mux.HandleFunc("GET /api/agents/{id}/review/comments", s.listReviewComments)
	s.mux.HandleFunc("POST /api/agents/{id}/review/comments", s.addReviewComment)
	s.mux.HandleFunc("POST /api/agents/{id}/review/comments/{cid}/resolve", s.resolveReviewComment)
	s.mux.HandleFunc("GET /api/prompts/recent", s.recentPrompts)
	s.mux.HandleFunc("POST /api/agents/{id}/preview/{port}", s.issuePreview)
	s.mux.HandleFunc("POST /api/agents/{id}/terminal", s.terminalOpen)
	s.mux.HandleFunc("GET /api/agents/{id}/terminal/{tid}/events", s.terminalEvents)
	s.mux.HandleFunc("POST /api/agents/{id}/terminal/{tid}/input", s.terminalInput)
	s.mux.HandleFunc("POST /api/agents/{id}/terminal/{tid}/resize", s.terminalResize)
	s.mux.HandleFunc("POST /api/agents/{id}/terminal/{tid}/release", s.terminalRelease)
	s.mux.HandleFunc("DELETE /api/agents/{id}/terminal/{tid}", s.terminalClose)
	s.mux.HandleFunc("POST /api/agents/{id}/merge", s.agentMerge)
	s.mux.HandleFunc("POST /api/agents/{id}/discard", s.agentDiscard)
	s.mux.HandleFunc("POST /api/agents/{id}/exit", s.agentExit)
	s.mux.HandleFunc("GET /api/agents/{id}/patch", s.agentPatch)
	s.mux.HandleFunc("GET /api/sessions", s.listSessions)
	// The index is a literal segment, so it is more specific than
	// GET /api/sessions/{id}/… and can never shadow a session id.
	s.mux.HandleFunc("GET /api/sessions/all", s.serveSessionIndex)
	s.mux.HandleFunc("POST /api/sessions", s.newSession)
	s.mux.HandleFunc("POST /api/sessions/{id}/load", s.loadSession)
	s.mux.HandleFunc("DELETE /api/sessions/{id}", s.deleteSession)
	s.mux.HandleFunc("POST /api/sessions/{id}/prompt", s.prompt)
	s.mux.HandleFunc("POST /api/sessions/{id}/cancel", s.cancel)
	s.mux.HandleFunc("POST /api/sessions/{id}/steer", s.steer)
	// The child controls: one live child of the named session, addressed by
	// the child ID its session reports. Steering and cancellation reach the
	// owning ACP session only; cancel reuses session/subagent_cancel.
	s.mux.HandleFunc("POST /api/sessions/{id}/subagents/{subagentId}/steer", s.steerSubagent)
	s.mux.HandleFunc("POST /api/sessions/{id}/subagents/{subagentId}/cancel", s.cancelSubagent)
	s.mux.HandleFunc("POST /api/sessions/{id}/mode", s.setMode)
	s.mux.HandleFunc("GET /api/sessions/{id}/access", s.getAccess)
	s.mux.HandleFunc("PATCH /api/sessions/{id}/access", s.patchAccess)
	s.mux.HandleFunc("GET /api/sessions/{id}/stack", s.sessionStack)
	s.mux.HandleFunc("GET /api/sessions/{id}/nodes/{nodeId}", s.sessionNode)
	s.mux.HandleFunc("GET /api/sessions/{id}/last-request", s.sessionLastRequest)
	s.mux.HandleFunc("GET /api/sessions/{id}/roster", s.sessionRoster)
	s.mux.HandleFunc("GET /api/sessions/{id}/step-diffs", s.sessionStepDiffs)
	s.libraryRoutes()
	s.recipeRoutes()
	s.scheduleRoutes()
	s.notificationRoutes()
	s.statusLinkRoutes()
	s.automationRoutes()
	s.modelsRoutes()
	s.budgetRoutes()
	s.watchRoutes()
	s.workspaceRoutes()
	s.projectSettingsRoutes()
	s.mux.HandleFunc("GET /api/runs", s.listRuns)
	s.mux.HandleFunc("GET /api/runs/{agentId}", s.getRun)
	s.mux.HandleFunc("POST /api/runs", s.startRun)
	s.mux.HandleFunc("POST /api/runs/{agentId}/answer", s.answerRun)
	s.mux.HandleFunc("POST /api/permissions/{toolCallId}", s.resolvePermission)
	s.mux.HandleFunc("POST /api/questions/{questionId}", s.resolveQuestion)
	s.mux.HandleFunc("GET /api/clients", s.listClients)
	s.mux.HandleFunc("POST /api/clients", s.createClient)
	s.mux.HandleFunc("DELETE /api/clients/{id}", s.deleteClient)
	// Factory grants are a Studio management surface. They hang off /api/, so
	// /mcp — which authenticates per client and is not under /api/ — can never
	// reach them: an MCP client cannot change its own authority.
	s.mux.HandleFunc("GET /api/clients/{clientId}/factory-grants/{projectId}", s.getFactoryGrant)
	s.mux.HandleFunc("PUT /api/clients/{clientId}/factory-grants/{projectId}", s.putFactoryGrant)
	// The built-in session's own access is the same kind of management
	// surface: it hangs off /api/, so /mcp can never reach it and a session's
	// model-facing tools cannot widen their own authority. Reads and writes
	// are versioned, and a field reset to unset restores inheritance.
	s.marshalAccessRoutes()
	s.mux.HandleFunc("GET /api/pending", s.listPending)
	s.mux.HandleFunc("POST /api/pending/{id}/approve", s.approvePending)
	s.mux.HandleFunc("POST /api/pending/{id}/deny", s.denyPending)
	s.mux.HandleFunc("GET /api/repos/{id}/issues", s.listRepoIssues)
	s.mux.HandleFunc("POST /api/repos/{id}/issues/{number}/spawn", s.spawnFromIssue)
	s.mux.HandleFunc("GET /api/disk", s.diskStatus)
	s.mux.HandleFunc("POST /api/prune", s.pruneDisk)
	s.mux.HandleFunc("GET /api/audit", s.listAudit)
	s.mux.HandleFunc("POST /api/workspaces/{name}/ca/rotate", s.rotateWorkspaceCA)
	s.mux.HandleFunc("POST /api/workspaces/{name}/shell", s.workspaceShellOpen)
	s.mux.HandleFunc("GET /api/workspaces/{name}/shell/{tid}/events", s.workspaceShellEvents)
	s.mux.HandleFunc("POST /api/workspaces/{name}/shell/{tid}/input", s.workspaceShellInput)
	s.mux.HandleFunc("POST /api/workspaces/{name}/shell/{tid}/resize", s.workspaceShellResize)
	s.mux.HandleFunc("DELETE /api/workspaces/{name}/shell/{tid}", s.workspaceShellClose)
	s.mux.HandleFunc("GET /api/credentials", s.listCredentials)
	s.mux.HandleFunc("POST /api/credentials", s.putCredential)
	s.mux.HandleFunc("DELETE /api/credentials/{id}", s.deleteCredential)
	s.mux.HandleFunc("GET /api/repos", s.listRepos)
	s.mux.HandleFunc("POST /api/repos", s.registerRepo)
	s.mux.HandleFunc("DELETE /api/repos/{id}", s.removeRepo)
	// The pull requests the bridge opened, and the one index of their checks.
	s.mux.HandleFunc("GET /api/pulls", s.listPulls)
	s.mux.HandleFunc("GET /api/pulls/{id}", s.getPull)
	s.mux.HandleFunc("POST /api/pulls/{id}/fix", s.fixPull)
	s.mux.HandleFunc("POST /api/pulls/{id}/fix/stop", s.stopPullFix)
	s.mux.HandleFunc("GET /api/network", s.networkView)
	s.mux.HandleFunc("GET /api/network/pending", s.networkPending)
	s.mux.HandleFunc("POST /api/network/decisions", s.networkDecision)
	s.mux.HandleFunc("GET /api/secrets/status", s.secretsStatus)
	s.mux.HandleFunc("GET /api/secrets", s.listSecrets)
	s.mux.HandleFunc("PUT /api/secrets/{ref...}", s.putSecret)
	s.mux.HandleFunc("DELETE /api/secrets/{ref...}", s.deleteSecret)
	s.discoveryRoutes()
	s.previewRoutes()
	if s.fleet != nil {
		s.mux.HandleFunc("GET /api/events", s.serveEvents)
	} else {
		s.mux.HandleFunc("GET /api/events", s.log.ServeSSE)
	}

	// Deliberately NOT under /api/: this endpoint authenticates per
	// client rather than as the listener. See authenticateMCP — it must
	// reject on its own.
	s.mux.HandleFunc("POST /mcp", s.mcpHandler)
}

// writeJSON encodes v with a status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr maps bridge errors onto status codes: unknown session →
// 404, stale permission/question resolve → 410, unsupported stack →
// 501, anything else from the child → 502.
func writeErr(w http.ResponseWriter, err error) {
	var unsupported ErrUnsupported
	var rpc *rpcError
	var budget ErrBudget
	var setup ErrSetupFailed
	var merge ErrWorkspaceMerge
	var running pullFixRunning
	var owned ErrSessionOwned
	switch {
	case errors.Is(err, ErrFactoryConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.As(err, &running):
		// The refusal names the fix that is already running.
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "fixId": running.ID})
	case errors.Is(err, ErrPullFixNotFailing), errors.Is(err, ErrInvalidPullFixMode):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrUnknownRecipe), errors.Is(err, ErrUnknownSchedule), errors.Is(err, ErrUnknownPull):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrInvalidRecipe), errors.Is(err, ErrRecipeBuiltin), errors.Is(err, errInvalidSchedule):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrRecipeExists):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrTemplateNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrTemplateName), errors.Is(err, ErrTemplatePool), errors.Is(err, ErrWorkspaceMountTarget):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrTemplateExists), errors.Is(err, ErrTemplateInUse), errors.Is(err, ErrBuildBusy), errors.Is(err, ErrTemplateNoDraft), errors.Is(err, ErrAgentNoWorkspace), errors.Is(err, ErrDraftChanged):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrWorkspaceNotBuilt):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "workspace_not_built"})
	case errors.Is(err, ErrUntrustedRepoTemplate):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrWorkspaceInvalid), errors.As(err, &merge):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	case errors.As(err, &setup):
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "code": "setup_failed", "output": setup.Output})
	case errors.Is(err, ErrPreviewPortNotDeclared):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrTooManyTerminals):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrUnknownTerminal):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, errTerminalInput):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrUnknownReroute):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, errRerouteConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.As(err, &budget):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "budget_exceeded", "scope": budget.Scope})
	case errors.Is(err, ErrUnknownSession):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.As(err, &owned):
		// The session exists but belongs to the bridge — an agent's, the
		// control agent's, a planner's — so opening it is a conflict with
		// its owner, not a missing session (P7 D5).
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrGone):
		writeJSON(w, http.StatusGone, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrUnknownAgent):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrUnknownRepo), errors.Is(err, ErrUnregisteredRepo):
		// The caller named a repo the bridge does not know. That is a
		// 404, not a gateway fault.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrUnknownPending):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrUnknownReviewComment):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, errScopeMismatch):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, errInvalidRun), errors.Is(err, errInvalidLibrary), errors.Is(err, errInvalidProjectSettings):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, errInvalidReview):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrOutsideWorkspace):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrWorkspaceNeedsRuntime):
		// The bridge has no container runtime: a limitation of how it
		// was started (a host-only daemon), not a gateway fault.
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": err.Error()})
	case errors.As(err, &unsupported):
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": unsupported.Error()})
	case errors.As(err, &rpc) && rpc.Code == rpcInvalidParams:
		// The agent refused the input itself (a bad scope, an unknown
		// provider), which is the caller's mistake, not a gateway fault.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": rpc.Message})
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
}

// decodeJSON reads a size-limited JSON body into v: oversize bodies
// and trailing data after the first value are rejected. Returns false
// (and writes 400) on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must contain a single JSON value"})
		return false
	}
	return true
}

// decodeStrictJSON is decodeJSON for a body whose every field is known: an
// unknown field is a typo, and dropping it silently is worse than refusing it.
func decodeStrictJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must contain a single JSON value"})
		return false
	}
	return true
}

// config exposes read-only bridge configuration needed by the SPA.
func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		writeJSON(w, http.StatusOK, map[string]string{"cwdRoot": s.reg.RootCwd})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": s.fleet.ProjectStatus()})
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.projectsWithFactoryIDs())
}

// projectsWithFactoryIDs is the project list with each entry's derived ids
// attached: its stable factory project ID (what a grant is keyed by and what
// an MCP client may name) and the registered repo its work comes from (what
// `GET /api/repos/{id}/issues` is keyed by). Every other field is exactly
// what the fleet reported.
func (s *Server) projectsWithFactoryIDs() []contract.ProjectStatus {
	statuses := s.fleet.ProjectStatus()
	for i := range statuses {
		if id, ok := s.fleet.ws.ProjectID(statuses[i].Root); ok {
			statuses[i].MCPProjectID = id
		}
		statuses[i].RepoID = s.fleet.projectRepoID(statuses[i].Root)
	}
	return statuses
}

func (s *Server) addProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Root string `json:"root"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := ValidateProjectRoot(body.Root); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.fleet.ws.AddProject(body.Root); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.fleet.ProjectStatus())
}

func (s *Server) removeProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Root string `json:"root"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Root == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "root is required"})
		return
	}
	if err := s.fleet.StopProject(body.Root); err != nil {
		writeErr(w, err)
		return
	}
	for _, a := range s.fleet.ws.Agents() {
		if a.Project == body.Root {
			s.fleet.dropGate(a.ID)
		}
	}
	// Removal drops the project's identity and every grant naming it in one
	// save, so it takes the policy lock: an authorization either sees the
	// project or does not, and never sees it after its grants are gone.
	if err := s.factoryAuth.withPolicy(func() error { return s.fleet.ws.RemoveProject(body.Root) }); err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.emit(contract.ProjectRemovedDelta{Kind: contract.DeltaProjectRemoved, Project: body.Root})
	writeJSON(w, http.StatusOK, s.fleet.ProjectStatus())
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.fleet.Snapshot())
}

// inbox answers the fleet's inbox: the four agent groups and the needs-you
// queue, computed fresh on every request (P5 F2). A bridge built over a
// registry rather than a fleet has no inbox, so the route answers exactly
// what the mux answers for a path it does not know.
func (s *Server) inbox(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s.fleet.Inbox())
}

// ValidateProjectRoot checks that root is an absolute path under one of the
// bridge's trusted roots. It is exported for CLI startup validation.
func ValidateProjectRoot(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("project root %q must be an absolute path", root)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolved = root
	}
	bases := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		if p, err := filepath.EvalSymlinks(home); err == nil {
			bases = append(bases, p)
		}
	}
	tmp := os.Getenv("TMPDIR")
	if tmp == "" {
		tmp = "/tmp"
	}
	if p, err := filepath.EvalSymlinks(tmp); err == nil {
		bases = append(bases, p)
	}
	if wd, err := os.Getwd(); err == nil {
		if p, err := filepath.EvalSymlinks(wd); err == nil {
			bases = append(bases, p)
		}
	}
	for _, base := range bases {
		rel, err := filepath.Rel(base, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("project root %q is outside the trusted-roots allow-list (your home directory, %s, or the bridge's working directory)", root, tmp)
}

func (s *Server) spawnAgent(w http.ResponseWriter, r *http.Request) {
	var body contract.SpawnRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := ValidateProjectRoot(body.Project); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// A request that leaves a field out takes the project's default.
	defaults := s.fleet.ws.ProjectSettingsFor(body.Project)
	isolated := defaults.Isolated != nil && *defaults.Isolated
	if body.Isolated != nil {
		isolated = *body.Isolated
	}
	if body.Mode == "" {
		body.Mode = defaults.Mode
	}
	if body.Auto == nil {
		body.Auto = defaults.Auto
	}
	if len(body.Routing) == 0 || string(body.Routing) == "null" {
		body.Routing = defaults.Routing
	}
	if body.Workspace == "" {
		body.Workspace = defaults.Workspace
	}
	id, err := s.fleet.Spawn(r.Context(), body.Project, SpawnOptions{
		Name: body.Name, Mode: body.Mode, Auto: body.Auto, Isolated: isolated, Branch: body.Branch, BaseRef: body.BaseRef,
		Routing: body.Routing, Workspace: body.Workspace,
	})
	if id == "" {
		writeErr(w, err)
		return
	}
	if body.Prompt != "" {
		if rt, e := s.fleet.RuntimeForSession(id); e == nil {
			go func() {
				if e := rt.reg.Prompt(context.Background(), rt.sessionID, body.Prompt); e != nil {
					slog.Default().Warn("webbridge: first prompt failed", "agent", id, "err", e)
				}
			}()
		}
	}
	resp := contract.SpawnResult{AgentID: id}
	if err != nil {
		resp.Warning = err.Error()
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) agentDiff(w http.ResponseWriter, r *http.Request) {
	raw, err := s.fleet.Diff(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) agentFiles(w http.ResponseWriter, r *http.Request) {
	raw, err := s.fleet.Files(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, raw)
}

func (s *Server) agentFile(w http.ResponseWriter, r *http.Request) {
	raw, err := s.fleet.File(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, raw)
}

func (s *Server) agentCommitDraft(w http.ResponseWriter, r *http.Request) {
	raw, err := s.fleet.CommitDraft(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, raw)
}

// agentVerify runs the verify gate now and returns the stored record.
func (s *Server) agentVerify(w http.ResponseWriter, r *http.Request) {
	rec, err := s.fleet.RunGate(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// agentGate returns the stored verify record, or 204 when none has run.
func (s *Server) agentGate(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.fleet.Gate(r.PathValue("id"))
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) agentMerge(w http.ResponseWriter, r *http.Request) {
	var body contract.MergeRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	raw, err := s.fleet.Merge(r.Context(), r.PathValue("id"), body.CommitMessage)
	if err != nil {
		writeErr(w, err)
		return
	}
	// A refusal carries a reason and merged:false. Map it to 409 so the UI
	// branches on a code rather than parsing prose.
	var probe struct {
		Merged bool   `json:"merged"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &probe)
	status := http.StatusOK
	if !probe.Merged && probe.Reason != "" {
		status = http.StatusConflict
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func (s *Server) agentDiscard(w http.ResponseWriter, r *http.Request) {
	if err := s.fleet.Discard(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contract.ActionStatus{Status: "discarded"})
}

func (s *Server) agentExit(w http.ResponseWriter, r *http.Request) {
	var body contract.ExitRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	res, err := s.fleet.Exit(r.Context(), r.PathValue("id"), ExitOptions{
		CommitMessage: body.CommitMessage,
		Override:      body.Override,
		NoPR:          body.NoPR,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) agentPatch(w http.ResponseWriter, r *http.Request) {
	patch, err := s.fleet.Patch(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/x-patch")
	w.Header().Set("Content-Disposition", `attachment; filename="marshal-`+r.PathValue("id")+`.patch"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(patch)
}

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("stream") == "fleet" {
		s.fleet.FleetLog().ServeSSEKey(w, r, fleetStreamKey)
		return
	}
	id := r.URL.Query().Get("sessionId")
	if id == "" {
		http.Error(w, "bridge: sessionId or stream=fleet is required", http.StatusBadRequest)
		return
	}
	log, err := s.fleet.LogForSession(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	log.ServeSSE(w, r)
}

// registryForSession resolves an id to the owning Registry, its event log, and
// the ACP session id to address registry calls with. A fleet resolves it
// through ResolveSession, the one rule, so every per-session route — an
// agent's session and a terminal's client session on its project child alike —
// reaches the same runtime (P7 D2). In the non-fleet path the id is already the
// session id.
func (s *Server) registryForSession(id string) (*Registry, *EventLog, string, error) {
	if s.fleet != nil {
		rs, err := s.fleet.ResolveSession(id)
		if err != nil {
			return nil, nil, "", err
		}
		return rs.Reg, rs.Log, rs.SessionID, nil
	}
	return s.reg, s.log, id, nil
}

func (s *Server) registryForRoot(root string) (*Registry, *EventLog, error) {
	if s.fleet != nil {
		rt, err := s.fleet.runtimeForRoot(root)
		if err != nil {
			return nil, nil, err
		}
		return rt.reg, rt.log, nil
	}
	return s.reg, s.log, nil
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cwd query parameter is required"})
		return
	}
	params := map[string]string{"cwd": cwd}
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		params["cursor"] = cursor
	}
	var reg *Registry
	var child *Child
	if s.fleet != nil {
		rt, err := s.fleet.runtimeForRoot(cwd)
		if err != nil {
			// No live agent is rooted at this project. That is the common
			// case for a project the bridge manages but has not used yet,
			// and the Sessions panel lists every project from
			// /api/projects, so fall back to the control agent's own
			// session for the root rather than failing the request.
			if !errors.Is(err, ErrUnknownProject) {
				writeErr(w, err)
				return
			}
			// The control child is asked directly. Building a Registry
			// over it would install that Registry's hooks on a child the
			// bridge already owns.
			child, err = s.fleet.controlChild(r.Context())
			if err != nil {
				writeErr(w, err)
				return
			}
		} else {
			agentCwd, err := rt.agentPath(cwd)
			if err != nil {
				writeErr(w, err)
				return
			}
			params["cwd"] = string(agentCwd)
			reg = rt.reg
		}
	} else {
		reg = s.reg
	}
	if child == nil {
		child = reg.child
	}
	res, err := child.Request(r.Context(), "session/list", params)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res)
}

// newSession creates (or resumes) a session via Registry.New.
func (s *Server) newSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cwd       string `json:"cwd"`
		SessionID string `json:"sessionId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Cwd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cwd is required"})
		return
	}
	var id string
	var err error
	if s.fleet != nil {
		// This is a spawn origin like any other, so it takes the project's
		// starting tuple: without it a session created here fell back to the
		// engine default and silently ignored the project's choice.
		defaults := s.fleet.ws.ProjectSettingsFor(body.Cwd)
		id, err = s.fleet.Spawn(r.Context(), body.Cwd, SpawnOptions{Mode: defaults.Mode, Auto: defaults.Auto})
	} else {
		id, err = s.reg.New(r.Context(), AgentPath(body.Cwd), body.SessionID)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sessionId": id})
}

// loadSession attaches to an existing session and returns the retained
// event tail so the SPA can render current state without an SSE
// reconnect. The registry tracks the session under the bridge's
// configured root cwd. A session the child no longer knows surfaces as
// the child's error (502), not 404 — the bridge cannot distinguish a
// child-side "session not found" from a transport failure.
func (s *Server) loadSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Cwd string `json:"cwd"`
	}
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &body) {
			return
		}
	}
	var reg *Registry
	var log *EventLog
	var sessionID string
	cwd := body.Cwd
	var agentCwd AgentPath
	if s.fleet != nil {
		rs, err := s.fleet.OpenSession(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		reg, log, sessionID = rs.Reg, rs.Log, rs.SessionID
		if rs.Agent == nil {
			// A project session is already open on its child's runtime, and
			// followed there: there is nothing for session/load to do, and the
			// log's tail is the answer the SPA gets (decision 5).
			events := log.Tail(sessionID)
			if events == nil {
				events = []Event{}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"sessionId": sessionID,
				"events":    events,
			})
			return
		}
		agentCwd, err = rs.Agent.agentPath(cwd)
		if err != nil {
			writeErr(w, err)
			return
		}
	} else {
		if cwd == "" {
			cwd = s.reg.RootCwd
		}
		reg, log, sessionID = s.reg, s.log, id
		agentCwd = AgentPath(cwd)
	}
	if cwd == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cwd is required (body or configured root)"})
		return
	}
	if err := reg.Load(r.Context(), agentCwd, sessionID); err != nil {
		writeErr(w, err)
		return
	}
	events := log.Tail(sessionID)
	if events == nil {
		events = []Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessionId": sessionID,
		"events":    events,
	})
}

// deleteSession removes the session record.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := reg.Delete(r.Context(), sessionID); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// prompt starts a turn. Registry.Prompt blocks until the turn
// completes, so it runs on its own goroutine; the HTTP round trip only
// confirms the turn was accepted. Turn output arrives over SSE, and
// the bridge-side turn_end event carries the terminal status.
func (s *Server) prompt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	reg, _, sessionID, err := s.registryForSession(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.fleet != nil {
		if err := s.fleet.budgetGate(s.fleet.agentIDOf(id)); err != nil {
			writeErr(w, err)
			return
		}
	}
	info, ok := reg.lookup(sessionID)
	if !ok {
		writeErr(w, ErrUnknownSession)
		return
	}
	if info.Busy {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "turn already in flight"})
		return
	}
	var body contract.PromptRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
		return
	}
	go func() {
		if err := reg.Prompt(context.Background(), sessionID, body.Text); err != nil {
			slog.Default().Warn("webbridge: prompt turn failed", "session", sessionID, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

// cancel interrupts the in-flight turn.
func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := reg.Cancel(r.Context(), sessionID); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// steer redirects the in-flight turn.
func (s *Server) steer(w http.ResponseWriter, r *http.Request) {
	var body contract.SteerRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
		return
	}
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := reg.Steer(r.Context(), sessionID, body.Text); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "steered"})
}

// The child-control outcomes writeSubagentErr separates, as the response
// body's "code". A caller must be able to tell them apart without parsing a
// sentence: a conflict is a request ID the session already used for something
// else, an unsupported backend registers no such control, an unknown delivery
// names no live child of this session, and a lost connection left the outcome
// unknown — the one case where a retry must carry the same request ID.
const (
	subagentCodeInvalid         = "invalid"
	subagentCodeUnknownSession  = "unknown_session"
	subagentCodeUnknownDelivery = "unknown_delivery"
	subagentCodeConflict        = "conflict"
	subagentCodeRefused         = "refused"
	subagentCodeUnsupported     = "unsupported"
	subagentCodeConnectionLost  = "connection_lost"
)

// rpcServerError is JSON-RPC's server-error code. ACP sends it for a session's
// own refusal, and the bridge may not import internal/acp, so the code is a
// literal here beside method-not-found and invalid-params.
const rpcServerError = -32000

// subagentIDPath reads the route's child ID. A child ID is an int64 on the
// wire and session-local; anything else is the caller's mistake and must not
// reach the agent, where 0 names no child and would answer "unknown" to a typo.
func subagentIDPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("subagentId")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "subagentId must be a positive integer: " + raw,
			"code":  subagentCodeInvalid,
		})
		return 0, false
	}
	return id, true
}

// steerSubagent forwards one user instruction to one live child of the named
// session (session/subagent_steer).
//
// The session is resolved first, which is the authorization step: a child ID
// is session-local, so two sessions that both hold child 1 each reach only
// their own. The route owns the session and child IDs and the body names only
// the request and its text, so a caller cannot address another session's child
// through either field.
//
// The bridge keeps no receipt of its own: the session deduplicates by request
// ID, so a retry after a lost reply is answered from its receipt instead of
// being enqueued twice. Accepted is not consumed, and nothing here upgrades
// one into the other.
func (s *Server) steerSubagent(w http.ResponseWriter, r *http.Request) {
	subagentID, ok := subagentIDPath(w, r)
	if !ok {
		return
	}
	var body struct {
		RequestID string `json:"requestId"`
		Message   string `json:"message"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.RequestID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "requestId is required", "code": subagentCodeInvalid})
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message is required", "code": subagentCodeInvalid})
		return
	}
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeSubagentErr(w, err, "subagent_steer")
		return
	}
	res, err := reg.call(r.Context(), sessionID, "session/subagent_steer", "subagent_steer", map[string]any{
		"subagentId": subagentID,
		"requestId":  body.RequestID,
		"message":    body.Message,
	})
	if err != nil {
		writeSubagentErr(w, err, "subagent_steer")
		return
	}
	var out struct {
		RequestID string `json:"requestId"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(res, &out); err != nil || out.RequestID == "" || out.Status == "" {
		// The call ended without an acceptance. Whether the child's queue took
		// the instruction is unknown, so this is a lost reply, not a success.
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "bridge: session/subagent_steer returned no acceptance",
			"code":  subagentCodeConnectionLost,
		})
		return
	}
	writeJSON(w, http.StatusOK, contract.SubagentSteerResult{RequestID: body.RequestID, Status: out.Status})
}

// cancelSubagent asks one live child of the named session to stop through the
// existing session/subagent_cancel control: a second route, never a second
// cancellation mechanism. Cancelled is the acknowledgement — the child's
// terminal state arrives as its own completion, so a caller must not read this
// as "stopped".
func (s *Server) cancelSubagent(w http.ResponseWriter, r *http.Request) {
	subagentID, ok := subagentIDPath(w, r)
	if !ok {
		return
	}
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeSubagentErr(w, err, "subagent_cancel")
		return
	}
	res, err := reg.call(r.Context(), sessionID, "session/subagent_cancel", "subagent_cancel", map[string]any{
		"subagentId": subagentID,
	})
	if err != nil {
		writeSubagentErr(w, err, "subagent_cancel")
		return
	}
	var out contract.SubagentCancelResult
	if err := json.Unmarshal(res, &out); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "bridge: decode session/subagent_cancel result: " + err.Error(),
			"code":  subagentCodeConnectionLost,
		})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// writeSubagentErr answers a failed child control with a body whose "code"
// tells the four apart: a conflict (409), an unsupported backend (501), an
// unknown delivery (404) and a lost connection (502). An unknown session is
// 404 before any control is attempted.
func writeSubagentErr(w http.ResponseWriter, err error, feature string) {
	var rpc *rpcError
	var unsupported ErrUnsupported
	switch {
	case errors.Is(err, ErrUnknownSession), errors.Is(err, ErrUnknownAgent):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error(), "code": subagentCodeUnknownSession})
	case errors.As(err, &unsupported):
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": unsupported.Error(), "code": subagentCodeUnsupported})
	case errors.As(err, &rpc) && rpc.Code == rpcInvalidParams:
		// The session refused the input itself.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": rpc.Message, "code": subagentCodeInvalid})
	case errors.As(err, &rpc) && rpc.Code == rpcServerError:
		// The session refused the request; its sentence says which refusal.
		code := subagentRefusalCode(rpc.Message)
		if code == subagentCodeUnknownDelivery {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": rpc.Message, "code": code})
			return
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": rpc.Message, "code": code})
	default:
		// No reply at all: the child stopped, the write failed, or the call was
		// cancelled. The request may have been accepted before the connection
		// went, so the outcome is unknown and a retry must reuse its ID.
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "code": subagentCodeConnectionLost})
	}
}

// subagentRefusalCode reads which refusal a session's sentence is. web/ is
// standard library only, so the bridge may not import the session's error
// values or a typed code: the sentences internal/app/session/subagents.go
// writes are the seam, and http_test.go pins each one. A sentence it does not
// recognise stays "refused" rather than being guessed into a conflict a client
// would act on.
func subagentRefusalCode(message string) string {
	switch {
	case strings.Contains(message, "was already used"):
		return subagentCodeConflict
	case strings.Contains(message, "unknown subagent"),
		strings.Contains(message, "has no live session"),
		strings.Contains(message, "no longer running"):
		return subagentCodeUnknownDelivery
	default:
		return subagentCodeRefused
	}
}

// setMode switches the session's permission mode. It is the legacy
// single-field ingress for PATCH /api/sessions/{id}/access: the mode is
// forwarded as an atomic patch and the full committed state is returned.
// New clients use the access routes and never need a dummy mode.
func (s *Server) setMode(w http.ResponseWriter, r *http.Request) {
	var body contract.ModeRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Mode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode is required"})
		return
	}
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := reg.SetAccess(r.Context(), sessionID, AccessPatch{Mode: &body.Mode})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// getAccess reports the session's committed access state
// (session/access_state). It is the authoritative read a client bootstraps
// from instead of inferring privileges from defaults or a transcript.
func (s *Server) getAccess(w http.ResponseWriter, r *http.Request) {
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := reg.AccessState(r.Context(), sessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// patchAccess applies one atomic access patch (session/set_mode) and returns
// the full committed state. The body is decoded strictly, so a null, unknown,
// wrong-typed or empty patch is rejected here and never forwarded; the bridge
// validates the wire shape only and grants nothing locally.
func (s *Server) patchAccess(w http.ResponseWriter, r *http.Request) {
	var patch AccessPatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := reg.SetAccess(r.Context(), sessionID, patch)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// writeRaw writes an agent result as-is.
func writeRaw(w http.ResponseWriter, result json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

// subagentParam reads the optional ?subagent= query value. A bad value
// writes 400 and returns ok=false.
func subagentParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.URL.Query().Get("subagent")
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid subagent"})
		return 0, false
	}
	return id, true
}

// sessionStack proxies the session's current stack snapshot.
func (s *Server) sessionStack(w http.ResponseWriter, r *http.Request) {
	sub, ok := subagentParam(w, r)
	if !ok {
		return
	}
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	result, err := reg.Stack(r.Context(), sessionID, sub)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, result)
}

// sessionNode proxies one node's full detail.
func (s *Server) sessionNode(w http.ResponseWriter, r *http.Request) {
	sub, ok := subagentParam(w, r)
	if !ok {
		return
	}
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	result, err := reg.StackNode(r.Context(), sessionID, r.PathValue("nodeId"), sub)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, result)
}

// sessionLastRequest proxies the newest model request.
func (s *Server) sessionLastRequest(w http.ResponseWriter, r *http.Request) {
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	result, err := reg.LastRequest(r.Context(), sessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, result)
}

// sessionRoster proxies the roster of live role bindings, for the Runs
// page's roles legend.
func (s *Server) sessionRoster(w http.ResponseWriter, r *http.Request) {
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	result, err := reg.Roster(r.Context(), sessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, result)
}

// sessionStepDiffs proxies the per-step diffs.
func (s *Server) sessionStepDiffs(w http.ResponseWriter, r *http.Request) {
	reg, _, sessionID, err := s.registryForSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	result, err := reg.StepDiffs(r.Context(), sessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRaw(w, result)
}

// resolvePermission delivers the SPA's decision to a pending
// session/request_permission.
func (s *Server) resolvePermission(w http.ResponseWriter, r *http.Request) {
	var body Decision
	if !decodeJSON(w, r, &body) {
		return
	}
	var err error
	if s.fleet != nil {
		err = s.fleet.ResolvePermission(r.PathValue("toolCallId"), body)
	} else {
		err = s.reg.ResolvePermission(r.PathValue("toolCallId"), body)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

// resolveQuestion delivers the SPA's answers to a pending
// session/request_question.
func (s *Server) resolveQuestion(w http.ResponseWriter, r *http.Request) {
	var body Answers
	if !decodeJSON(w, r, &body) {
		return
	}
	var err error
	if s.fleet != nil {
		err = s.fleet.ResolveQuestion(r.PathValue("questionId"), body)
	} else {
		err = s.reg.ResolveQuestion(r.PathValue("questionId"), body)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

// listClients returns all registered MCP clients, omitting token hashes.
// The hash has no business reaching the UI — it widens the blast radius
// of any XSS.
func (s *Server) listClients(w http.ResponseWriter, r *http.Request) {
	clients := s.fleet.Clients()
	out := make([]map[string]any, 0, len(clients))
	for _, c := range clients {
		out = append(out, map[string]any{
			"id":            c.ID,
			"name":          c.Name,
			"autonomous":    c.Autonomous,
			"maxConcurrent": c.MaxConcurrent,
			"maxPerDay":     c.MaxPerDay,
			"allowedRepos":  c.AllowedRepos,
			"ownerId":       c.OwnerID,
			"createdAt":     c.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// createClient mints a new MCP client token, persists only the hash,
// and returns the plaintext once.
func (s *Server) createClient(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string   `json:"name"`
		Autonomous    bool     `json:"autonomous"`
		MaxConcurrent int      `json:"maxConcurrent"`
		MaxPerDay     int      `json:"maxPerDay"`
		AllowedRepos  []string `json:"allowedRepos"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	plain, hash, err := NewClientToken()
	if err != nil {
		writeErr(w, err)
		return
	}
	c := MCPClient{
		ID:            newAgentID(),
		Name:          body.Name,
		TokenHash:     hash,
		Autonomous:    body.Autonomous,
		MaxConcurrent: body.MaxConcurrent,
		MaxPerDay:     body.MaxPerDay,
		AllowedRepos:  body.AllowedRepos,
		OwnerID:       DefaultOwnerID,
		CreatedAt:     time.Now().UTC(),
	}
	if err := s.fleet.ws.PutClient(c); err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditClientCreated, OwnerID: c.OwnerID,
		ClientID: c.ID, Detail: c.Name})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         c.ID,
		"name":       c.Name,
		"token":      plain,
		"autonomous": c.Autonomous,
	})
}

// deleteClient removes an MCP client.
func (s *Server) deleteClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	// Deleting a client revokes its grants in the same save, and it shares the
	// policy lock with authorization: a mutation either admits under the
	// client that still existed or is denied by its absence.
	if err := s.factoryAuth.withPolicy(func() error { return s.fleet.ws.DeleteClient(id) }); err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditClientRevoked, OwnerID: DefaultOwnerID, ClientID: id})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// listPending returns all pending submissions awaiting confirmation.
func (s *Server) listPending(w http.ResponseWriter, r *http.Request) {
	pending := s.fleet.ws.Pending()
	out := make([]contract.PendingSubmission, 0, len(pending))
	for _, p := range pending {
		out = append(out, pendingSubmission(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// pendingSubmission projects a stored pending spawn onto the wire shape
// GET /api/pending answers. OwnerID is deliberately left unset: today's
// bridge never reports one, and an absent value means the local owner.
func pendingSubmission(p PendingSpawn) contract.PendingSubmission {
	return contract.PendingSubmission{
		ID:        p.ID,
		Origin:    p.Origin,
		ClientID:  p.ClientID,
		Title:     p.Title,
		RepoID:    p.RepoID,
		Ref:       p.Ref,
		Prompt:    p.Prompt,
		Plan:      p.Plan,
		Mode:      p.Mode,
		Auto:      p.Auto,
		CreatedAt: p.CreatedAt,
		ExpiresAt: p.ExpiresAt,
	}
}

// approvePending confirms a submission and starts its agent.
func (s *Server) approvePending(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	agentID, err := s.fleet.Approve(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contract.ApproveResult{AgentID: agentID, Status: "running"})
}

// denyPending discards a submission without spawning.
func (s *Server) denyPending(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	if err := s.fleet.Deny(id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contract.ActionStatus{Status: "denied"})
}

// listRepoIssues returns the open issues for a registered repo.
func (s *Server) listRepoIssues(w http.ResponseWriter, r *http.Request) {
	issues, err := s.fleet.ListRepoIssues(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, issues)
}

// spawnFromIssue turns one issue into a submission.
func (s *Server) spawnFromIssue(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	if number == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "issue number is required"})
		return
	}
	var n int
	if _, err := fmt.Sscanf(number, "%d", &n); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "issue number must be an integer"})
		return
	}
	res, err := s.fleet.SubmitIssue(r.Context(), r.PathValue("id"), n)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}

// listAudit returns the most recent audit records, oldest first. The
// limit is bounded by maxAuditTail so a hostile or buggy client cannot
// force the whole log across the wire.
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxAuditTail {
		limit = maxAuditTail
	}
	events, err := s.fleet.audit.Tail(limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	if events == nil {
		events = []AuditEvent{}
	}
	writeJSON(w, http.StatusOK, events)
}

// marshalStateUnauthorized is the state a user-side access surface reports for
// a pair the server will not act on: a session that is not associated with the
// project it named, or a project this instance does not hold. It names neither
// the reason nor which of the two was wrong, so the route is not an oracle for
// which IDs exist.
const marshalStateUnauthorized = "unauthorized"

// MarshalAccessLayer is one level's stored built-in access and the opaque
// version a write to it must name. A level is the project's defaults when it
// carries no session, and one session's (or worker's) override when it does.
// Unset fields inherit the level beneath; an explicit false is a denial that
// stays one until it is reset.
type MarshalAccessLayer struct {
	Access  MarshalFactoryAccess `json:"access"`
	Version string               `json:"version"`
}

// MarshalAccessView is one session's Studio connection and effective built-in
// access, as the user-side surface reads it and as every write answers.
//
// State is the connection the surface reports: connected, ambiguous,
// unavailable, unsupported or unauthorized. The layers, the effective access
// and its Source are the policy, which is stored whether or not a connection
// is up: an offline state never implies an unreadable or unwritable policy.
type MarshalAccessView struct {
	State      string   `json:"state"`
	InstanceID string   `json:"instanceId,omitempty"`
	Generation string   `json:"generation,omitempty"`
	ProjectID  string   `json:"projectId,omitempty"`
	SessionID  string   `json:"sessionId,omitempty"`
	WorkerID   string   `json:"workerId,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	// SelectedProjectID is the project a person chose for a session whose
	// enrollment named more than one. SelectionVersion is the opaque version
	// a selection write must name.
	SelectedProjectID string              `json:"selectedProjectId,omitempty"`
	SelectionVersion  string              `json:"selectionVersion,omitempty"`
	Default           MarshalAccessLayer  `json:"default"`
	Override          *MarshalAccessLayer `json:"override,omitempty"`
	// OverrideVersion is the version a session-layer write must name. It is
	// present even when no override is stored: that level reports the stable
	// version of the absent value, so the first write from an editor that has
	// read it is conditional on exactly what it was shown.
	OverrideVersion string               `json:"overrideVersion,omitempty"`
	Effective       MarshalFactoryAccess `json:"effective"`
	Source          map[string]string    `json:"source,omitempty"`
	// Editable says the surface may write both layers. It follows the policy
	// the server will actually store, not the connection state.
	Editable bool `json:"editable"`
}

// price recomputes the effective access from the layers the view carries, in
// the order the authorizer resolves them. A write that replaces one layer calls
// it so the answer describes exactly the value that write left behind.
func (v *MarshalAccessView) price() {
	layers := []MarshalFactoryAccess{v.Default.Access}
	if v.Override != nil {
		layers = append(layers, v.Override.Access)
	}
	v.Effective = resolveMarshalFactoryAccess(layers...)
}

// marshalAccessRoutes registers the built-in session's user-side access
// surface. Every route hangs off /api/, so /mcp cannot reach one: the model
// a session runs holds no call that widens its own authority.
func (s *Server) marshalAccessRoutes() {
	s.mux.HandleFunc("GET /api/marshal/access", s.getMarshalAccess)
	s.mux.HandleFunc("PUT /api/marshal/access/default", s.putMarshalAccessDefaultRoute)
	s.mux.HandleFunc("PUT /api/marshal/access/override", s.putMarshalAccessOverrideRoute)
	s.mux.HandleFunc("PUT /api/marshal/access/selection", s.putMarshalSelectionRoute)
}

// getMarshalAccess answers GET /api/marshal/access. projectId names the project
// whose defaults to read; sessionId (and optionally workerId) names the
// session whose override and connection state to report. A session with no
// project resolves the project from the server's own associations, reporting
// ambiguous when they name more than one.
func (s *Server) getMarshalAccess(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	view, err := s.marshalAccessView(q.Get("projectId"), q.Get("sessionId"), q.Get("workerId"))
	if err != nil {
		factoryAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// putMarshalAccessDefaultRoute answers PUT /api/marshal/access/default: the
// whole project-default layer plus the version it was edited from.
func (s *Server) putMarshalAccessDefaultRoute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID string               `json:"projectId"`
		Access    MarshalFactoryAccess `json:"access"`
		Version   string               `json:"version"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := validateMarshalAccessLayer(body.Access); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(body.Version) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version is required"})
		return
	}
	wrote, state, err := s.factoryAuth.putMarshalAccessDefault(body.ProjectID, body.Access, body.Version)
	if err != nil {
		factoryAuthError(w, err)
		return
	}
	view, err := s.marshalAccessView(body.ProjectID, "", "")
	if err != nil {
		factoryAuthError(w, err)
		return
	}
	// The layer this write left behind is the one the store reported, not a
	// second read: a racing writer must not make the answer describe its value.
	view.Default = MarshalAccessLayer{Access: state.Access, Version: state.Version}
	view.price()
	status := http.StatusOK
	if !wrote {
		// The layer moved since the caller read it. The answer carries the
		// value that won, so the editor shows the conflict instead of
		// re-reading blind and discarding what the user typed.
		status = http.StatusConflict
	}
	writeJSON(w, status, view)
}

// putMarshalAccessOverrideRoute answers PUT /api/marshal/access/override: one
// session's (or worker's) override and the version it was edited from. An
// access with every field unset is the reset: it stores a level that inherits
// everything, which is what a revoke means.
func (s *Server) putMarshalAccessOverrideRoute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID string               `json:"projectId"`
		SessionID string               `json:"sessionId"`
		WorkerID  string               `json:"workerId"`
		Access    MarshalFactoryAccess `json:"access"`
		Version   string               `json:"version"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := validateMarshalAccessLayer(body.Access); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(body.SessionID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sessionId is required"})
		return
	}
	if strings.TrimSpace(body.Version) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version is required"})
		return
	}
	wrote, state, err := s.factoryAuth.putMarshalAccessOverride(body.ProjectID, body.SessionID, body.WorkerID, body.Access, body.Version)
	if err != nil {
		factoryAuthError(w, err)
		return
	}
	view, err := s.marshalAccessView(body.ProjectID, body.SessionID, body.WorkerID)
	if err != nil {
		factoryAuthError(w, err)
		return
	}
	view.Override = &MarshalAccessLayer{Access: state.Access, Version: state.Version}
	view.price()
	status := http.StatusOK
	if !wrote {
		status = http.StatusConflict
	}
	writeJSON(w, status, view)
}

// putMarshalSelectionRoute answers PUT /api/marshal/access/selection: the
// project a person picked for a session whose enrollment was ambiguous. An
// empty projectId clears the choice, which is the reset: the session is
// ambiguous again and the surface says so.
func (s *Server) putMarshalSelectionRoute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"sessionId"`
		ProjectID string `json:"projectId"`
		Version   string `json:"version"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.SessionID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sessionId is required"})
		return
	}
	if strings.TrimSpace(body.Version) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version is required"})
		return
	}
	wrote, state, err := s.putMarshalSelection(body.SessionID, body.ProjectID, body.Version)
	if err != nil {
		factoryAuthError(w, err)
		return
	}
	view, err := s.marshalAccessView(body.ProjectID, body.SessionID, "")
	if err != nil {
		factoryAuthError(w, err)
		return
	}
	view.SelectedProjectID, view.SelectionVersion = state.ProjectID, state.Version
	status := http.StatusOK
	if !wrote {
		status = http.StatusConflict
	}
	writeJSON(w, status, view)
}

// putMarshalSelection stores one session's selected project under the policy
// lock, so a choice cannot land inside an admitted mutation. A project that is
// not one of the session's candidates is refused: a selection answers the
// question the server asked, it does not introduce a project.
func (s *Server) putMarshalSelection(sessionID, projectID, expected string) (bool, marshalFactorySelectionState, error) {
	if s == nil || s.factoryAuth == nil {
		return false, marshalFactorySelectionState{}, fmt.Errorf("%w: no authorizer", ErrFactoryUnavailable)
	}
	if projectID != "" && !s.marshalIsCandidate(sessionID, projectID) {
		return false, marshalFactorySelectionState{}, fmt.Errorf("%w: project is not a candidate for this session", errFactoryGrantInvalid)
	}
	var (
		wrote bool
		state marshalFactorySelectionState
	)
	err := s.factoryAuth.withPolicy(func() error {
		next, current, err := s.factoryAuth.ws.putMarshalSelectionIfMatch(sessionID, projectID, expected)
		wrote, state = next, current
		return err
	})
	if err != nil {
		return false, state, err
	}
	return wrote, state, nil
}

// validateMarshalAccessLayer refuses a layer whose fields no resolution could
// honour. An unset authority is inheritance, so it is not checked; a named one
// must be one of the three, and asking for approval without any authority is a
// contradictory request rather than a stricter one.
func validateMarshalAccessLayer(a MarshalFactoryAccess) error {
	if a.ApprovalAuthority != nil {
		switch *a.ApprovalAuthority {
		case factoryAuthorityNone, factoryAuthorityUserDirected, factoryAuthorityStanding:
		case "":
			return fmt.Errorf("%w: approvalAuthority must be none, user_directed or standing", errFactoryGrantInvalid)
		default:
			return fmt.Errorf("%w: approvalAuthority must be none, user_directed or standing", errFactoryGrantInvalid)
		}
	}
	if a.ApproveAndQueue != nil && *a.ApproveAndQueue && a.ApprovalAuthority != nil && *a.ApprovalAuthority == factoryAuthorityNone {
		return fmt.Errorf("%w: approveAndQueue requires user_directed or standing authority", errFactoryGrantInvalid)
	}
	return nil
}

// marshalAccessView builds the user-side view of one session's Studio
// connection and effective access.
//
// The connection state is the server's own reading of what it tracks — the
// instance identity it reports and the associations it persisted — never a
// value a request supplies. The policy below it is read from the store whatever
// the state is: a session that is offline, ambiguous or unauthorized still has
// stored defaults, and an operator may still change them.
func (s *Server) marshalAccessView(projectID, sessionID, workerID string) (MarshalAccessView, error) {
	if s == nil || s.fleet == nil || s.fleet.ws == nil {
		return MarshalAccessView{}, fmt.Errorf("%w: no workspace", ErrFactoryUnavailable)
	}
	ws := s.fleet.ws
	view := MarshalAccessView{SessionID: sessionID, WorkerID: workerID}
	instanceID, generation, ok := s.marshalInstance()
	if !ok {
		// Not a daemon instance, or one that names no socket: there is no
		// Studio to be connected to, and inventing an identity would bind
		// nothing. The policy is still readable, so the defaults are below.
		view.State = marshalStateUnsupported
		projectID = s.marshalDefaultProject(projectID)
		s.fillMarshalAccessLayers(&view, projectID)
		return view, nil
	}
	view.InstanceID, view.Generation = instanceID, generation
	view.Candidates = s.marshalCandidateProjects(sessionID)
	if selection, ok := ws.marshalSelection(sessionID); ok {
		view.SelectedProjectID, view.SelectionVersion = selection.ProjectID, selection.Version
	} else {
		view.SelectionVersion = marshalSelectionState(sessionID, "").Version
	}
	resolved, state := s.marshalResolveProject(projectID, sessionID, view.Candidates, view.SelectedProjectID)
	view.State = state
	if resolved == "" {
		// A state that names no project still reads the project the caller
		// named, when it named one: the defaults are the project's setting,
		// not the session's, and an operator may change them while the
		// session is offline, ambiguous or unauthorized.
		if projectID != "" {
			s.fillMarshalAccessLayers(&view, projectID)
		}
		return view, nil
	}
	view.ProjectID = resolved
	s.fillMarshalAccessLayers(&view, resolved)
	return view, nil
}

// marshalDefaultProject is the project whose defaults a surface reads when the
// server is not a daemon instance and the caller named none: the first
// registered project, which is the only one such a server can hold.
func (s *Server) marshalDefaultProject(projectID string) string {
	if projectID != "" || s == nil || s.fleet == nil || s.fleet.ws == nil {
		return projectID
	}
	projects := s.fleet.ws.Projects()
	if len(projects) == 0 {
		return ""
	}
	if id, ok := s.fleet.ws.ProjectID(projects[0]); ok {
		return id
	}
	return ""
}

// marshalResolveProject picks the project a view is about and the connection
// state that goes with it. An empty result is a state that names no project.
func (s *Server) marshalResolveProject(projectID, sessionID string, candidates []string, selected string) (string, string) {
	named := strings.TrimSpace(projectID)
	if named == "" {
		switch {
		case len(candidates) == 0:
			// The server tracks no project for this session: there is no
			// enrollment to report and none can be made by asking.
			return "", marshalStateUnavailable
		case len(candidates) == 1:
			return candidates[0], marshalStateConnected
		default:
			if selected != "" && slices.Contains(candidates, selected) {
				return selected, marshalStateConnected
			}
			return "", marshalStateAmbiguous
		}
	}
	if s.fleet == nil || s.fleet.ws == nil {
		return "", marshalStateUnavailable
	}
	if _, ok := s.fleet.ws.ProjectRootByID(named); !ok {
		return "", marshalStateUnauthorized
	}
	if len(candidates) == 0 {
		// The server tracks no project for this session, so nothing verifies
		// that it acts on the one the caller named.
		return "", marshalStateUnavailable
	}
	if !slices.Contains(candidates, named) {
		// The session is tracked, and it is not this project's.
		return "", marshalStateUnauthorized
	}
	return named, marshalStateConnected
}

// fillMarshalAccessLayers reads the stored layers for one project (and, when
// the view names a session, that session's override) and resolves them.
func (s *Server) fillMarshalAccessLayers(view *MarshalAccessView, projectID string) {
	if view == nil || s == nil || s.fleet == nil || s.fleet.ws == nil || projectID == "" {
		return
	}
	ws := s.fleet.ws
	if _, ok := ws.ProjectRootByID(projectID); !ok {
		return
	}
	view.ProjectID = projectID
	view.Editable = true
	// An absent default is not an empty one: it reports the zero layer and the
	// stable version of it, so an editor's first write is conditional on
	// exactly what it was shown.
	project := marshalAccessState(projectID, "", "", MarshalFactoryAccess{})
	source := map[string]string{"project": "default"}
	if stored, ok := ws.marshalAccessDefault(projectID); ok {
		project = stored
		source["project"] = "override"
	}
	view.Default = MarshalAccessLayer{Access: project.Access, Version: project.Version}
	view.Override = nil
	if view.SessionID != "" {
		// The version an editor holds is the version of the scope it edits: a
		// worker's panel writes the worker's record, never its session's. What
		// it displays may still be inherited from the session, until the worker
		// has a record of its own.
		view.OverrideVersion = marshalAccessState(projectID, view.SessionID, view.WorkerID, MarshalFactoryAccess{}).Version
		if override, ok := ws.marshalAccessOverride(projectID, view.SessionID, view.WorkerID); ok && marshalLayerApplies(override, view.WorkerID) {
			source["session"] = "override"
			if override.WorkerID == view.WorkerID {
				// This scope's own record, so this is what a save is
				// conditional on.
				view.OverrideVersion = override.Version
			}
			view.Override = &MarshalAccessLayer{Access: override.Access, Version: view.OverrideVersion}
		}
	}
	view.Source = source
	view.price()
}

// marshalLayerApplies reports whether a stored override is the one this view's
// session (or worker) holds. A record that names no worker is the session's
// own; one that names a worker is that worker's, and a sibling worker inherits
// the project default instead.
func marshalLayerApplies(override marshalFactoryAccessState, workerID string) bool {
	return override.WorkerID == "" || override.WorkerID == workerID
}

// marshalCandidateProjects is every project the server's own associations name
// for sessionID, resolved to opaque project IDs and sorted. It replaces the
// request as the source of what a session may be bound to.
func (s *Server) marshalCandidateProjects(sessionID string) []string {
	if sessionID == "" || s == nil || s.fleet == nil || s.fleet.ws == nil {
		return nil
	}
	var out []string
	for _, assoc := range s.marshalAssociations(sessionID) {
		if id, ok := s.fleet.ws.ProjectID(assoc.root); ok && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// marshalIsCandidate reports whether projectID is one of the session's
// candidates. A session the server does not track has none, so no selection can
// be made for it.
func (s *Server) marshalIsCandidate(sessionID, projectID string) bool {
	return slices.Contains(s.marshalCandidateProjects(sessionID), projectID)
}
