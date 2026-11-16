package bridge

import (
	"encoding/json"
	"net/http"
	"strings"
)

// The factory tools serve two kinds of authenticated principal, and the same
// tool must authorize whichever one is calling it.
//
// An external MCP client is authorized by its stored grant and repository
// allowlist. A built-in session is authorized by the binding the owner-only
// socket established: its project is the binding's, and its capabilities are
// that session's effective access — the project defaults with the session's or
// worker's override applied — resolved under the policy lock on every call, so
// a changed default or a revocation takes effect at the next operation.
//
// These helpers are where that choice is made, once, so no tool can pick the
// wrong authority by forgetting to branch. The identity is never read from a
// tool argument: it is the client the transport authenticated, resolved against
// the live enrollment the same way authenticateMCPPrincipal resolved it.

// toolPrincipal is the identity a tool call from this client acts as: a built-in
// session when the client holds a live enrollment, and an external client
// otherwise.
func (s *Server) toolPrincipal(c MCPClient) mcpPrincipal {
	if binding, ok := s.marshalBindingFor(c.ID); ok {
		return mcpPrincipal{Client: c, Binding: binding, Builtin: true}
	}
	return mcpPrincipal{Client: c}
}

// authorizeToolRoot resolves the project root one read may act on and checks the
// read capability that gates it. A built-in session may name only the project
// its binding names; an external client only a project its grant covers. An
// unknown project and a project the principal holds nothing on are one answer,
// and no root leaves this function.
func (s *Server) authorizeToolRoot(clientID, projectID string) (string, error) {
	if strings.TrimSpace(projectID) == "" {
		return "", errFactoryToolInput
	}
	if s.factoryAuth == nil || s.fleet == nil || s.fleet.ws == nil {
		return "", ErrFactoryUnavailable
	}
	if binding, ok := s.marshalBindingFor(clientID); ok {
		if projectID != binding.ProjectID {
			// A session is scoped by the project its binding names; a tool
			// argument may not move it to another one.
			return "", errFactoryDenied
		}
		if err := s.factoryAuth.AuthorizeMarshalCapability(binding, "", factoryCapabilityRead); err != nil {
			return "", err
		}
		root, ok := s.fleet.ws.ProjectRootByID(projectID)
		if !ok {
			return "", errFactoryDenied
		}
		return root, nil
	}
	state, err := s.factoryAuth.grantState(clientID, projectID)
	if err != nil || !s.factoryAuth.grantAllows(state.Grant, factoryCapabilityRead) {
		return "", errFactoryDenied
	}
	root, ok := s.fleet.ws.ProjectRootByID(projectID)
	if !ok {
		return "", errFactoryDenied
	}
	return root, nil
}

// authorizeToolRead checks the read capability for one item's repository. The
// project is checked with it, so a built-in session cannot read another
// project's item through an id it guessed.
func (s *Server) authorizeToolRead(clientID, projectID, repoID string) error {
	if binding, ok := s.marshalBindingFor(clientID); ok {
		if projectID != binding.ProjectID {
			return errFactoryDenied
		}
		return s.factoryAuth.AuthorizeMarshalCapability(binding, repoID, factoryCapabilityRead)
	}
	return s.factoryAuth.AuthorizeRead(clientID, projectID, repoID)
}

// withAuthorizedToolMutation authorizes one durable mutation for whichever
// principal this client is and, only when it holds, runs fn while the policy
// lock is still held — so the journal append is the linearization point for a
// grant or an access change, and a revocation lands entirely before the
// operation was admitted or entirely after it committed.
//
// fn receives the principal to journal under. For an external client that is
// its client identity and claimed authority; for a built-in session it is the
// instance, project, session and worker the binding verified.
func (s *Server) withAuthorizedToolMutation(c MCPClient, projectID, repoID, capability, requestID, authority string, fn func(factoryPrincipal) (FactoryItem, error)) (FactoryItem, error) {
	if binding, ok := s.marshalBindingFor(c.ID); ok {
		if projectID != binding.ProjectID {
			return FactoryItem{}, errFactoryDenied
		}
		return s.factoryAuth.WithAuthorizedMarshalMutation(binding, repoID, capability, func(p factoryPrincipal) (FactoryItem, error) {
			p.RequestID = requestID
			return fn(p)
		})
	}
	return s.factoryAuth.WithAuthorizedMutation(c.ID, projectID, repoID, capability, func() (FactoryItem, error) {
		return fn(factoryToolPrincipal(c.ID, projectID, capability, requestID, authority))
	})
}

// factoryToolProjectsBuiltin lists the one project a verified built-in session
// acts on and the capabilities its effective access holds there.
//
// The project is the binding's and the capabilities are resolved from the stored
// default plus this session's override, under the policy lock, so what a session
// is told it may do is what the next call will enforce. A session whose access
// is being changed therefore sees the change at its next listing.
func (s *Server) factoryToolProjectsBuiltin(w http.ResponseWriter, principal mcpPrincipal, id json.RawMessage) {
	status, err := s.factoryAuth.marshalStatus(principal.Binding)
	if err != nil {
		writeFactoryToolFailure(w, id, factoryFailureOf(err))
		return
	}
	projects := []map[string]any{}
	if status.ProjectID != "" {
		capabilities := []string{}
		for _, capability := range []string{
			factoryCapabilityRead, factoryCapabilityCreateDraft, factoryCapabilityEditDraft,
			factoryCapabilityAnswerQuestion, factoryCapabilityApproveAndQueue,
		} {
			if marshalAccessAllows(status.Access, capability) {
				capabilities = append(capabilities, capability)
			}
		}
		entry := map[string]any{"projectId": status.ProjectID, "capabilities": capabilities}
		if marshalAccessAllows(status.Access, factoryCapabilityApproveAndQueue) && status.Access.ApprovalAuthority != nil {
			// The authority an approval acts under, so a caller can see it
			// without guessing. It is the stored policy, never the request's.
			entry["approvalAuthority"] = *status.Access.ApprovalAuthority
		}
		projects = append(projects, entry)
	}
	writeFactoryToolResult(w, id, map[string]any{"projects": projects})
}
