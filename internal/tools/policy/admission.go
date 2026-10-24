package policy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"

	"marshal/internal/interaction"
)

// ErrStaleAdmission reports that the admission context changed between
// evaluation and execution. The caller must re-evaluate and retry.
var ErrStaleAdmission = errors.New("admission context changed")

// ErrApprovalRequired reports that the operation needs a specific approval
// that has not been granted.
var ErrApprovalRequired = errors.New("specific approval required")

// Call is one logical tool invocation to evaluate.
type Call struct {
	InvocationID string
	ToolName     string
	Args         map[string]any
	Shell        *ShellInput
	Targets      []ShellTarget
}

// CommandAttempt is one managed-command attempt to admit.
type CommandAttempt struct {
	InvocationID string
	ToolName     string
	Input        ShellInput
}

// Authorization is the opaque proof produced by Evaluate and consumed by one
// admission. It binds the invocation identity, the exact command bytes, the
// cwd, the workspace and the access revision.
type Authorization struct {
	snapshot          interaction.Snapshot
	call              Call
	fingerprint       [32]byte
	mu                sync.Mutex
	approvedRequestID string
	consumed          bool
}

// Admission is one accepted execution boundary. Commit consumes the proof
// exactly once; Release is idempotent and unlocks even after a failed commit.
type Admission struct {
	lease         interaction.AdmissionLease
	authorization *Authorization
	committed     bool
	release       sync.Once
}

// Authorizer binds a policy engine to one live interaction authority.
type Authorizer struct {
	engine    *PolicyEngine
	authority interaction.Authority
}

// NewAuthorizer builds an authorizer over the given engine and authority.
func NewAuthorizer(engine *PolicyEngine, authority interaction.Authority) *Authorizer {
	return &Authorizer{engine: engine, authority: authority}
}

// Evaluate captures a fresh authority snapshot and evaluates the call against
// the live configuration. The returned proof binds the exact facts.
func (a *Authorizer) Evaluate(ctx context.Context, call Call) (*Authorization, Evaluation, error) {
	if a.authority == nil {
		return nil, Evaluation{}, errors.New("policy: no interaction authority")
	}
	snap := a.authority.Snapshot()
	var analysis *ShellAnalysis
	if call.Shell != nil {
		an, err := AnalyzeShell(*call.Shell)
		if err != nil {
			return nil, Evaluation{}, err
		}
		analysis = &an
	}
	ev, err := a.engine.EvaluateInput(Input{
		ToolName:      call.ToolName,
		Args:          call.Args,
		ActorState:    snap.Access,
		Shell:         analysis,
		Targets:       call.Targets,
		WorkspaceRoot: snap.WorkspaceRoot,
	})
	if err != nil {
		return nil, Evaluation{}, err
	}
	auth := &Authorization{snapshot: snap, call: call, fingerprint: fingerprintCall(call)}
	return auth, ev, nil
}

// ApproveExact marks the captured Confirm as approved by one specific request.
// It cannot approve a Deny or a missing-capability result.
func (a *Authorizer) ApproveExact(auth *Authorization, requestID string) (*Authorization, error) {
	if auth == nil {
		return nil, ErrApprovalRequired
	}
	auth.mu.Lock()
	defer auth.mu.Unlock()
	if auth.consumed {
		return nil, ErrStaleAdmission
	}
	auth.approvedRequestID = requestID
	return auth, nil
}

// BeginTool revalidates the proof against the live facts and takes the owner
// admission lease. A mismatch fails closed.
func (a *Authorizer) BeginTool(ctx context.Context, call Call) (*Admission, error) {
	auth, ok := authorizationFromContext(ctx)
	if !ok {
		return nil, ErrApprovalRequired
	}
	if a.authority == nil {
		return nil, ErrApprovalRequired
	}
	if fingerprintCall(call) != auth.fingerprint {
		return nil, ErrStaleAdmission
	}
	auth.mu.Lock()
	if auth.consumed {
		auth.mu.Unlock()
		return nil, ErrStaleAdmission
	}
	approved := auth.approvedRequestID != ""
	auth.mu.Unlock()
	// Re-evaluate against the live authority and configuration before taking
	// the lease.
	live := a.authority.Snapshot()
	var analysis *ShellAnalysis
	if call.Shell != nil {
		an, err := AnalyzeShell(*call.Shell)
		if err != nil {
			return nil, err
		}
		analysis = &an
	}
	ev, err := a.engine.EvaluateInput(Input{
		ToolName:      call.ToolName,
		Args:          call.Args,
		ActorState:    live.Access,
		Shell:         analysis,
		Targets:       call.Targets,
		WorkspaceRoot: live.WorkspaceRoot,
	})
	if err != nil {
		return nil, err
	}
	if ev.Decision == DecisionDeny {
		return nil, ErrApprovalRequired
	}
	if ev.Required.System && !live.Access.System {
		return nil, ErrApprovalRequired
	}
	if ev.Decision == DecisionConfirm && !approved {
		return nil, ErrApprovalRequired
	}
	lease, err := a.authority.BeginAdmission(ctx, auth.snapshot)
	if err != nil {
		return nil, err
	}
	return &Admission{lease: lease, authorization: auth}, nil
}

// BeginCommand admits one managed-command attempt.
func (a *Authorizer) BeginCommand(ctx context.Context, attempt CommandAttempt) (*Admission, error) {
	call := Call{InvocationID: attempt.InvocationID, ToolName: attempt.ToolName, Shell: &attempt.Input}
	return a.BeginTool(ctx, call)
}

// Commit consumes the proof exactly once.
func (adm *Admission) Commit() error {
	if adm == nil || adm.authorization == nil {
		return ErrApprovalRequired
	}
	adm.authorization.mu.Lock()
	defer adm.authorization.mu.Unlock()
	if adm.authorization.consumed {
		return ErrStaleAdmission
	}
	adm.authorization.consumed = true
	return nil
}

// Release unlocks the admission lease. It is idempotent.
func (adm *Admission) Release() {
	if adm == nil {
		return
	}
	adm.release.Do(func() {
		if adm.lease != nil {
			adm.lease.Release()
		}
	})
}

type authContextKey struct{}

// WithAuthorization attaches an authorization proof to a context.
func WithAuthorization(ctx context.Context, auth *Authorization) context.Context {
	return context.WithValue(ctx, authContextKey{}, auth)
}

func authorizationFromContext(ctx context.Context) (*Authorization, bool) {
	auth, ok := ctx.Value(authContextKey{}).(*Authorization)
	return auth, ok
}

// InvocationID returns the logical invocation identity carried by the proof.
func InvocationID(ctx context.Context) string {
	auth, ok := authorizationFromContext(ctx)
	if !ok {
		return ""
	}
	return auth.call.InvocationID
}

// ShellInputFromContext returns the shell facts the attached proof captured.
//
// A tool that admits an execution must admit exactly the facts dispatch
// evaluated: re-deriving the command, directory, workspace or environment can
// silently describe a different command than the one that was approved. When a
// proof is attached, its captured input is that authority.
func ShellInputFromContext(ctx context.Context) (ShellInput, bool) {
	auth, ok := authorizationFromContext(ctx)
	if !ok || auth.call.Shell == nil {
		return ShellInput{}, false
	}
	return *auth.call.Shell, true
}

// fingerprintCall hashes the exact invocation facts. Raw command bytes are
// retained; they are never shell-normalized.
//
// Every fact the analyzer reads is bound, not just the command: the home
// directory, read roots and environment steer effect and scope classification,
// so a proof that omitted them would describe a different command than the one
// that was approved. encoding/json orders map keys deterministically, which is
// what makes the shell environment and tool arguments hashable.
func fingerprintCall(call Call) [32]byte {
	payload := struct {
		InvocationID string
		ToolName     string
		Shell        *ShellInput
		Args         map[string]any
	}{call.InvocationID, call.ToolName, call.Shell, call.Args}
	b, err := json.Marshal(payload)
	if err != nil {
		// Unmarshalable arguments must not silently collapse two distinct
		// invocations: fall back to the identity fields, which are always
		// compared, and let the live re-evaluation decide.
		b = []byte(call.InvocationID + "\x00" + call.ToolName)
	}
	return sha256.Sum256(b)
}
