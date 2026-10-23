package policy

import (
	"errors"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

// ShellInput is everything the analyzer needs to resolve one command.
type ShellInput struct {
	Command       string
	Dir           string
	WorkspaceRoot string
	HomeDir       string
	ReadRoots     []string
	Env           map[string]string
}

// ShellWord is one decoded shell word. Text is the printed form; Value is the
// decoded literal when Known. SingleField reports whether the word expands to
// exactly one field (no unquoted glob or word splitting).
type ShellWord struct {
	Text        string
	Value       string
	Known       bool
	SingleField bool
}

// ShellFlag is one parsed flag with an optional value.
type ShellFlag struct {
	Name     string
	Value    ShellWord
	HasValue bool
}

// ShellScope classifies where a target lives relative to the actor workspace.
type ShellScope string

const (
	ShellScopeNone      ShellScope = "none"
	ShellScopeWorkspace ShellScope = "workspace"
	ShellScopeReadRoot  ShellScope = "read_root"
	ShellScopeExternal  ShellScope = "external"
	ShellScopeUnknown   ShellScope = "unknown"
)

// ShellEffect is the bit set of effects one operation can have.
type ShellEffect uint16

const (
	ShellRead ShellEffect = 1 << iota
	ShellWrite
	ShellDestructive
	ShellEnvironment
	ShellPublish
	ShellPrivilege
	ShellUnknown
	ShellCatastrophic
)

// ShellTarget is one resolved path target of an operation.
type ShellTarget struct {
	Word           ShellWord
	Path           string
	Scope          ShellScope
	Effect         ShellEffect
	WorkspaceRoot  bool
	EntireRootHome bool
	RawDisk        bool
}

// ReasonCode is a stable policy reason identifier. Message wording never
// controls policy.
type ReasonCode string

const (
	ReasonRead                ReasonCode = "shell.read"
	ReasonEdit                ReasonCode = "shell.edit"
	ReasonBuildTest           ReasonCode = "shell.build_test"
	ReasonLocalInstall        ReasonCode = "shell.local_install"
	ReasonDestructive         ReasonCode = "shell.destructive"
	ReasonEnvironment         ReasonCode = "shell.environment"
	ReasonPublishing          ReasonCode = "shell.publishing"
	ReasonWorkspaceRootDelete ReasonCode = "shell.workspace_root_delete"
	ReasonFloorFormat         ReasonCode = "shell.floor.format"
	ReasonFloorRawDisk        ReasonCode = "shell.floor.raw_disk"
	ReasonFloorRootHome       ReasonCode = "shell.floor.root_home"
	ReasonFloorShutdown       ReasonCode = "shell.floor.shutdown"
	ReasonPrivilegeWrapper    ReasonCode = "shell.privilege_wrapper"
	ReasonUnknownExecutable   ReasonCode = "shell.unknown_executable"
	ReasonUnknownFlags        ReasonCode = "shell.unknown_flags"
	ReasonUnknownTarget       ReasonCode = "shell.unknown_target"
	ReasonUnknownPayload      ReasonCode = "shell.unknown_payload"
	ReasonDepthLimit          ReasonCode = "shell.depth_limit"
	ReasonExplicitDeny        ReasonCode = "rule.deny"
	ReasonExplicitAllow       ReasonCode = "rule.allow"
	ReasonExplicitConfirm     ReasonCode = "rule.confirm"
	ReasonRequiresAct         ReasonCode = "access.requires_act"
	ReasonRequiresSystem      ReasonCode = "access.requires_system"
)

// PolicyReason is one reason attached to an operation or evaluation.
type PolicyReason struct {
	Code        ReasonCode
	OperationID int
	Rule        string
	Message     string
}

// ShellOperation is one executable operation found in a command.
type ShellOperation struct {
	ID          int
	Start       uint
	End         uint
	Executable  ShellWord
	Subcommand  string
	Argv        []ShellWord
	Flags       []ShellFlag
	Operands    []ShellWord
	Directories []string
	Targets     []ShellTarget
	Effects     ShellEffect
	Reasons     []PolicyReason
	// Redirect marks an operation that is only a write redirect (a statement
	// with no command word). It has no executable to classify; its Effects are
	// already ShellWrite and its Operands hold the redirect targets.
	Redirect bool
}

// ShellAnalysis is the full parse of one command.
type ShellAnalysis struct {
	Input      ShellInput
	Operations []ShellOperation
	// NoStatements reports a parsed command with nothing to execute: empty, or
	// comments only. Every other command must analyze to at least one
	// operation; an empty Operations list for a command with statements is a
	// gap in the walker, and evaluation fails closed on it.
	NoStatements bool
}

// CapabilityRequirement is the interaction capability an evaluation needs.
type CapabilityRequirement struct {
	Act    bool
	System bool
}

// Evaluation is the outcome of evaluating one tool input.
type Evaluation struct {
	Decision             Decision
	Required             CapabilityRequirement
	Reasons              []PolicyReason
	ExactCommandApproval bool
}

// ConfigSource returns the current config, session rules and access revision.
type ConfigSource func() (config.Config, []string, uint64)

// Input is one tool invocation to evaluate.
type Input struct {
	ToolName   string
	Args       map[string]any
	ActorState interaction.State
	Shell      *ShellAnalysis
	Targets    []ShellTarget
}

// ErrStalePolicy reports that the policy snapshot changed under an evaluation.
var ErrStalePolicy = errors.New("policy snapshot changed")
