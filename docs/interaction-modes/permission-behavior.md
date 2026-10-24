# Act and Full Auto permissions

Updated 2026-10-24. This contract supersedes the approval and Auto sections of
the original interaction-modes feature spec.

Act executes the user's requested operations without implicit permission
prompts. This includes file changes, builds, tests, scripts, network and MCP
tools, Git checkout/switch, destructive workspace operations, and publishing.
It can ask clarification questions when the answer materially changes the
work. Explicit user-authored confirmation rules still apply, and broader
filesystem/environment access requires System.

Full Auto is Act with all operation confirmations preauthorized and no
questions. Enabling it also grants System access in the same state change;
configured Full Auto applies that grant when a session starts or reloads.
Turning Auto off leaves the conversation's System grant enabled until
`/system off`. Grants are session-scoped, never restored from history.

Full Auto proceeds through skill/spec/plan review workflows without waiting
for user approval. Inspected `/sdd new` candidates are accepted automatically;
invalid candidates remain on disk with inspection errors. Authoring does not
start execution. A pipeline that cannot resolve a human gate finishes with a
concrete blocker instead of opening a question panel. Subagent model consent
and skill-load prompts are also preauthorized.

Explicit deny rules, catastrophic-operation denials, privileged-shell-wrapper
denials, sandbox restrictions, and child capability ceilings remain enforced.
Ordinary approvals cannot grant missing capabilities. Commands are still
parsed, checked against live authority, and admitted with an invocation-bound,
single-use proof. Scope requirements cover every operation in a mixed command.

Privileged commands use the existing `shell.sudo` path. In Full Auto, an
available noninteractive sudo credential may be used; a required password is
reported as a blocker without opening a credential prompt.

Enabling Full Auto while a tool approval, question, skill-load prompt, or
subagent model-consent prompt is pending wakes that waiter. Command approvals
are reevaluated against the new authority before execution, so an old prompt
does not leave a shell invocation with a stale proof.

A requested exit-postmortem agent pass automatically receives temporary Act +
System access, including when the conversation ended in Ask. Ordinary work
stays closed, and the previous mode, Auto, and System settings are restored
after the pass's work is joined, on success, failure, or cancellation. Mode
changes and config reloads are refused while this temporary grant is active.
The explicit `/postmortem --agent` command grants Act + System for its follow-up
turn and discloses that grant alongside the report path.
