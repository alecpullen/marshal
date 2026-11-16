package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Enrollment is the one place a built-in session's identity is established. A
// Marshal session asks the daemon it runs on to bind it to the project the
// server already tracks for it; the daemon answers with a short-lived,
// project-scoped credential that only the trusted transport ever holds.
//
// Nothing in the request decides who the caller is. The request names the
// launching root and the session to enroll, and the server resolves the
// association itself — from the workspace's agents and from the sessions its
// project children track. A model-supplied path or identity is never a
// binding: an unregistered root or an unknown session is refused, and no
// request can name a project it is not associated with.
//
// The binding lives in memory only (marshalEnrollments). A restart therefore
// ends every enrollment: the persisted client record survives with no binding,
// which makes it an external client with no grants — it fails closed.

// marshalEnrollPath is the enrollment route. It is served on the owner-only
// socket handler alone: the network listener answers it 404, so a credential
// for a built-in session is minted only where the owner is the caller.
const marshalEnrollPath = "/api/marshal/enroll"

// marshalClientPrefix marks the client record enrollment minted, so a surface
// can tell a built-in session's credential apart from an external MCP client's.
const marshalClientPrefix = "marshal:"

// marshalEnrollmentTTL is how long an issued credential lives. It is scoped to
// one session and one project, and a session re-enrolls rather than extends it:
// the credential is deliberately shorter-lived than a session may be.
const marshalEnrollmentTTL = 12 * time.Hour

// The enrollment states MarshalConnectionStatus does not already name. Every
// one of them grants nothing.
const (
	marshalStateUnavailable = "unavailable"
	marshalStateAmbiguous   = "ambiguous"
	marshalStateUnsupported = "unsupported"
)

// errMarshalEnrollmentRefused is the answer to a request whose session, project
// or worker the server's own association does not agree with. It is one opaque
// value: a refusal says nothing about which part was wrong, and names no
// identity the caller did not already present.
var errMarshalEnrollmentRefused = errors.New("marshal enrollment refused")

// MarshalEnrollmentRequest is what a Marshal session asks the daemon to bind:
// the launching root as the internal side resolved it (Marshal's project
// identity anchors at the git root) and the session to enroll. It carries no
// identity the caller could choose.
type MarshalEnrollmentRequest struct {
	ProjectRoot string `json:"projectRoot,omitempty"`
	SessionID   string `json:"sessionId"`
}

// MarshalEnrollmentResponse is what the daemon answers. State is the one
// authoritative outcome; a state other than "connected" carries no token and
// grants nothing. Token is shown once, to the trusted transport.
type MarshalEnrollmentResponse struct {
	State      string   `json:"state"` // connected, ambiguous, unsupported, unavailable
	InstanceID string   `json:"instanceId,omitempty"`
	Generation string   `json:"generation,omitempty"`
	ProjectID  string   `json:"projectId,omitempty"`
	SessionID  string   `json:"sessionId,omitempty"`
	WorkerID   string   `json:"workerId,omitempty"`
	ItemID     string   `json:"itemId,omitempty"`
	Role       string   `json:"role,omitempty"`
	Token      string   `json:"token,omitempty"`
	ExpiresAt  string   `json:"expiresAt,omitempty"` // RFC3339 UTC
	Candidates []string `json:"candidates,omitempty"`
	// Access is the effective authority the binding carries, resolved the way
	// the authorizer resolves it, so a surface can report it without a second
	// round trip.
	Access MarshalFactoryAccess `json:"access"`
	Source map[string]string    `json:"source,omitempty"`
}

// marshalAssociation is one server-owned association between a session and the
// project it belongs to. agent is set for a fleet agent's session (and carries
// the factory identity the server spawned it with); it is empty for a plain
// project session, which the daemon tracks per project child.
type marshalAssociation struct {
	root   string
	agent  string
	itemID string
	role   string
}

// marshalEnrollmentRecord is one live enrollment: the binding the server
// derived and when the credential it issued lapses.
type marshalEnrollmentRecord struct {
	binding   MarshalSessionBinding
	expiresAt time.Time
}

// marshalEnrollments is the server's record of live enrollments, keyed by the
// client ID the credential belongs to. It is deliberately in memory: a restart
// must invalidate every enrollment, and a record that outlived its generation
// would be authority nobody revalidated.
type marshalEnrollments struct {
	mu       sync.Mutex
	byClient map[string]marshalEnrollmentRecord
}

func newMarshalEnrollments() *marshalEnrollments {
	return &marshalEnrollments{byClient: make(map[string]marshalEnrollmentRecord)}
}

func (e *marshalEnrollments) put(clientID string, binding MarshalSessionBinding, expiresAt time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.byClient[clientID] = marshalEnrollmentRecord{binding: binding, expiresAt: expiresAt}
}

// live reports the binding one client ID holds on generation now, and drops the
// record when it has expired or the daemon's generation has moved on. A
// generation change is the restart (or owner change) that revokes it: the
// answer is a denial, and the stale record is gone rather than left to be
// revalidated by every later request.
func (e *marshalEnrollments) live(clientID, generation string, now time.Time) (MarshalSessionBinding, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	record, ok := e.byClient[clientID]
	if !ok {
		return MarshalSessionBinding{}, false
	}
	if record.binding.Generation != generation || !now.Before(record.expiresAt) {
		delete(e.byClient, clientID)
		return MarshalSessionBinding{}, false
	}
	return record.binding, true
}

// revoke drops one client's enrollment, reporting whether there was one.
func (e *marshalEnrollments) revoke(clientID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.byClient[clientID]
	delete(e.byClient, clientID)
	return ok
}

// len reports how many enrollments are live, which a test reads to prove a
// refusal minted nothing.
func (e *marshalEnrollments) len() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.byClient)
}

// marshalEnrollmentClientID is the client ID one binding's credential belongs
// to. It is derived from the binding, so re-enrolling the same session replaces
// its record instead of accumulating one credential per attempt.
func marshalEnrollmentClientID(binding MarshalSessionBinding) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		binding.InstanceID, binding.ProjectID, binding.SessionID, binding.WorkerID,
	}, "\x00")))
	return marshalClientPrefix + hex.EncodeToString(h[:12])
}

// marshalGeneration is the daemon generation a credential is scoped to. It
// changes on a restart or an owner change — the two events that must invalidate
// what was bound before them.
func marshalGeneration(pid int, startedAt int64) string {
	return fmt.Sprintf("%d:%d", pid, startedAt)
}

// marshalInstance is this server's instance identity: the daemon's canonical
// socket path and its generation, both as GET /api/daemon reports them. A
// server that is not a daemon names no instance, and enrollment is unsupported
// there rather than bound to something made up.
func (s *Server) marshalInstance() (instanceID, generation string, ok bool) {
	if s == nil || s.daemonInfo == nil {
		return "", "", false
	}
	instanceID = strings.TrimSpace(s.daemonInfo.Socket)
	if instanceID == "" {
		return "", "", false
	}
	return instanceID, marshalGeneration(s.daemonInfo.PID, s.daemonInfo.StartedAt), true
}

// marshalAssociations is every server-owned association that names sessionID:
// the agents whose session it is (a factory worker among them, with the item
// and role the server spawned it for) and the project children that track it.
// It reads only what the daemon persisted; nothing comes from the request.
func (s *Server) marshalAssociations(sessionID string) []marshalAssociation {
	if s == nil || s.fleet == nil || sessionID == "" {
		return nil
	}
	var out []marshalAssociation
	for _, a := range s.fleet.ws.Agents() {
		if a.SessionID != sessionID || strings.TrimSpace(a.Project) == "" {
			continue
		}
		out = append(out, marshalAssociation{root: a.Project, agent: a.ID, itemID: a.FactoryItemID, role: a.FactoryRole})
	}
	for _, child := range s.projectChildren() {
		for _, session := range child.Sessions {
			if session.ID == sessionID {
				out = append(out, marshalAssociation{root: child.Root})
				break
			}
		}
	}
	return out
}

// enrollMarshal answers POST /api/marshal/enroll on the owner-only socket.
func (s *Server) enrollMarshal(w http.ResponseWriter, r *http.Request) {
	var req MarshalEnrollmentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	out, err := s.enrollMarshalSession(req)
	if err != nil {
		marshalEnrollmentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// enrollMarshalSession resolves one enrollment and issues its credential. Every
// answer that grants nothing is a state, so the caller reports it without
// treating it as a fault; only a request the server refuses outright is an
// error.
func (s *Server) enrollMarshalSession(req MarshalEnrollmentRequest) (MarshalEnrollmentResponse, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return MarshalEnrollmentResponse{State: marshalStateUnsupported}, nil
	}
	instanceID, generation, ok := s.marshalInstance()
	if !ok {
		// Not a daemon, or a daemon that names no socket: there is no instance
		// identity to bind to, and inventing one would bind nothing.
		return MarshalEnrollmentResponse{State: marshalStateUnsupported}, nil
	}
	if s.fleet == nil {
		return MarshalEnrollmentResponse{State: marshalStateUnavailable}, nil
	}

	associations := s.marshalAssociations(sessionID)
	if len(associations) == 0 {
		// The daemon does not run this session. A request cannot introduce
		// one, so this is a refusal rather than a status.
		return MarshalEnrollmentResponse{}, errMarshalEnrollmentRefused
	}

	// The request's root may only ever agree with what the association already
	// says. It selects among the associations that agree with it; it never
	// introduces a project the association does not name.
	requested, rootRegistered := "", false
	if root := strings.TrimSpace(req.ProjectRoot); root != "" {
		requested, rootRegistered = s.fleet.ws.ProjectID(root)
	}

	type candidate struct {
		assoc     marshalAssociation
		projectID string
	}
	var candidates []candidate
	for _, assoc := range associations {
		projectID, ok := s.fleet.ws.ProjectID(assoc.root)
		if !ok {
			// The association names a root this workspace did not register:
			// there is no opaque project ID to bind, and that is not something
			// a request can fix.
			continue
		}
		if rootRegistered && projectID != requested {
			continue
		}
		candidates = append(candidates, candidate{assoc: assoc, projectID: projectID})
	}
	if len(candidates) == 0 {
		if rootRegistered {
			// The request named a project the tracked association does not
			// agree with. That is a disagreement, not an unavailable instance.
			return MarshalEnrollmentResponse{}, errMarshalEnrollmentRefused
		}
		return MarshalEnrollmentResponse{State: marshalStateUnavailable}, nil
	}

	// One project per enrollment. More than one is the ambiguity the caller
	// must resolve: granting either would be a guess about which project this
	// session acts on.
	picked := candidates[0]
	var projectIDs []string
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		if seen[c.projectID] {
			// A worker's own association (the item and role the server spawned
			// it for) is more specific than the project child that also tracks
			// its session, so it wins.
			if picked.assoc.itemID == "" && c.assoc.itemID != "" {
				picked = c
			}
			continue
		}
		seen[c.projectID] = true
		projectIDs = append(projectIDs, c.projectID)
	}
	if len(projectIDs) > 1 {
		return MarshalEnrollmentResponse{
			State:      marshalStateAmbiguous,
			InstanceID: instanceID,
			Generation: generation,
			SessionID:  sessionID,
			Candidates: projectIDs,
		}, nil
	}

	// Only a factory worker carries a worker identity: a plain agent session is
	// the session itself, and naming its agent as a worker would apply a worker
	// override that was written for a different scope.
	workerID := ""
	if picked.assoc.itemID != "" {
		workerID = picked.assoc.agent
	}
	binding := MarshalSessionBinding{
		InstanceID: instanceID,
		ProjectID:  picked.projectID,
		SessionID:  sessionID,
		WorkerID:   workerID,
		ItemID:     picked.assoc.itemID,
		Role:       picked.assoc.role,
		Generation: generation,
	}
	return s.issueMarshalCredential(binding)
}

// issueMarshalCredential mints one binding's credential and records it. The
// client record is persisted (so the credential authenticates at /mcp); the
// binding is not, so a restart leaves the record with no authority at all.
func (s *Server) issueMarshalCredential(binding MarshalSessionBinding) (MarshalEnrollmentResponse, error) {
	plain, hash, err := NewClientToken()
	if err != nil {
		return MarshalEnrollmentResponse{}, err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(marshalEnrollmentTTL)
	clientID := marshalEnrollmentClientID(binding)
	if err := s.fleet.ws.PutClient(MCPClient{
		ID:        clientID,
		Name:      "Marshal session " + binding.SessionID,
		TokenHash: hash,
		OwnerID:   DefaultOwnerID,
		CreatedAt: now,
	}); err != nil {
		return MarshalEnrollmentResponse{}, err
	}
	s.marshalEnroll.put(clientID, binding, expiresAt)

	out := MarshalEnrollmentResponse{
		State:      marshalStateConnected,
		InstanceID: binding.InstanceID,
		Generation: binding.Generation,
		ProjectID:  binding.ProjectID,
		SessionID:  binding.SessionID,
		WorkerID:   binding.WorkerID,
		ItemID:     binding.ItemID,
		Role:       binding.Role,
		Token:      plain,
		ExpiresAt:  expiresAt.Format(time.RFC3339),
	}
	// The access the binding carries is the authorizer's own answer, not a
	// second opinion: a status surface shows exactly what the tools will check.
	if status, err := s.factoryAuth.marshalStatus(binding); err == nil {
		out.Access = status.Access
		out.Source = status.Source
	}
	return out, nil
}

// marshalBindingFor reports the verified binding one client holds right now.
// Only a client whose enrollment is live on the daemon's current generation has
// one; every other client — including an enrollment's record from before a
// restart — has none and keeps exactly the external semantics it had.
func (s *Server) marshalBindingFor(clientID string) (MarshalSessionBinding, bool) {
	if s == nil || s.marshalEnroll == nil || !strings.HasPrefix(clientID, marshalClientPrefix) {
		return MarshalSessionBinding{}, false
	}
	_, generation, ok := s.marshalInstance()
	if !ok {
		return MarshalSessionBinding{}, false
	}
	return s.marshalEnroll.live(clientID, generation, time.Now())
}

// revokeMarshalClient ends one enrollment and the client record it minted. It
// is what a credential that no longer authorizes anything is reduced to: the
// record fails closed immediately rather than lingering as a token with no
// grants.
func (s *Server) revokeMarshalClient(clientID string) {
	if s == nil {
		return
	}
	if s.marshalEnroll != nil {
		s.marshalEnroll.revoke(clientID)
	}
	if s.fleet != nil {
		_ = s.fleet.ws.DeleteClient(clientID)
	}
}

// marshalEnrollmentError maps enrollment's failures onto statuses. A refusal is
// one opaque sentence and names nothing, so the route cannot be used to learn
// which projects or sessions exist.
func marshalEnrollmentError(w http.ResponseWriter, err error) {
	if errors.Is(err, errMarshalEnrollmentRefused) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": errMarshalEnrollmentRefused.Error()})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "marshal enrollment unavailable"})
}

// mcpPrincipal is the identity one authenticated /mcp request acts as: an
// external MCP client, or a built-in session whose enrollment is live on this
// generation. Builtin is what separates the two, and Binding is the verified
// association the built-in path authorizes against — it is never read from a
// tool argument.
type mcpPrincipal struct {
	Client  MCPClient
	Binding MarshalSessionBinding
	Builtin bool
}

// mcpPrincipalKey is the request context key mcpHandler attaches the resolved
// principal under, so a tool path reached from it authorizes against the same
// verified identity the transport authenticated.
type mcpPrincipalKey struct{}

func withMCPPrincipal(r *http.Request, principal mcpPrincipal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), mcpPrincipalKey{}, principal))
}

// mcpPrincipalFrom reports the principal mcpHandler authenticated this request
// as. A tool path that reaches a request without one has no identity to act as.
func mcpPrincipalFrom(r *http.Request) (mcpPrincipal, bool) {
	if r == nil {
		return mcpPrincipal{}, false
	}
	principal, ok := r.Context().Value(mcpPrincipalKey{}).(mcpPrincipal)
	return principal, ok
}
