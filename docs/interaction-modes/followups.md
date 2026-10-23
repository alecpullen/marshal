# Interaction Modes — Review Residue

Items identified during branch review of `interaction-modes`
(base `4335c0d6`, head of the reviewed range `1b355e64`) and deliberately not
changed in the review-response pass. Each is recorded with what it is, why it
was left open, and what closing it would involve.

Everything the review marked as a defect or a fail-open was fixed in the
response pass; what follows is either a design decision that is the owner's to
make or a cleanup whose value does not justify the churn immediately before a
merge.

## 1. Routine shell writes outside the workspace are not System-gated

The policy matrix in `feature-spec.md` has the row:

| Routine file edit outside the workspace | Require System, then allow |
|---|---|

That row is implemented for non-shell tools (`file.write` and friends confirm on
risk) and for *destructive* shell operations
(`destructiveOutsideWorkspace` → `Required.System`), but not for ordinary shell
writes. `cp x /etc/y`, `mv x /opt/`, `tee /etc/motd` and `rm /etc/x` are Allow
in Act with no gate, no System requirement and no reason code.

This is the residue of two intended changes rather than an oversight in one
place: Act allows routine workspace writes, and the shell analyzer assigns a
single `ShellWrite` effect without per-command knowledge of *which* operands are
write targets.

Closing it needs write-target selection, not a target walk. `op.Targets`
currently classifies every operand, and for many commands the operands are not
paths at all — `git commit -m "fix the parser"`, `make build`,
`go test ./pkg/...` — so a naive "any non-workspace target requires System"
rule would demand System approval for ordinary commits and builds. A correct
implementation needs, per command family, which operands are write destinations
plus the redirect destinations, and it needs its own matrix tests.

Scope if picked up: `internal/tools/policy/{shell_scope,evaluation}.go`, the
effect/target tables, and `docs/interaction-modes/feature-spec.md` §Policy
matrix once the implementation and the row agree.

## 2. Auto is more permissive than it was for three destructive shapes

Deliberate per "no blanket rules" plus "Auto preauthorizes destructive workspace
operations", test-pinned, and listed here so the change is signed off rather
than discovered later:

- `git reset --hard` and `git clean -f` inside the workspace are Allow under
  Act + Auto. The retired rules made both a hard Deny in every non-system mode.
- `curl … | sh` and a dynamic argv0 (`${x}cmd`) moved from hard Deny to
  Confirm-exact.

Net effect for a user who already granted Auto: two destructive workspace
operations now proceed without a prompt, and two network/unknown shapes still
require an exact approval. The workspace preauthorization contract covers the
first pair; the second pair is strictly safer than before.

## 3. `toolRisk` fails open when no registry is wired

`toolRisk` returns `read_only` when the engine has no tool registry, so in Ask
mode *every* non-shell tool — including `file.write` — evaluates as Allow.
Production wires a registry (`internal/app/app.go`), so the branch is not
reachable in a real session, and two tests pin the fallback as intentional
legacy behaviour (`TestEvaluate_NoRegistryKeepsLegacyLowRiskAllow`,
`TestAskNetworkAndMCPRequirePositiveReadClassification`).

The review suggested `workspace_write` as the safer default. That contradicted
both tests, so it was reverted rather than forced. The better closure is to
remove the no-registry path entirely — make the registry a construction
requirement — so the fail-open cannot be reached and the two legacy tests can be
deleted with it.

## 4. `canonicalModeTuple`'s translation note never reaches a surface

The function returns `(mode, auto, note, error)`, where the note is user-facing
prose such as `mode "edit" replaced with ask; approval for every file change was
retired`. All call sites discard it (`_`). The recipe `Diagnostic` channel
reports only *invalid* spellings, never translations, so a migrated value
changes silently.

Either drop the return, or surface it where a notice channel already exists (the
recipe diagnostic being the obvious one). Left open because the second option is
a small UX feature and the first touches eleven call sites for no behavioural
gain.

## 5. Write-only analysis fields

`policy.Call.Targets`, `policy.Input.Targets` and `ShellTarget.Effect` are
written but never read: `EvaluateInput` ignores `Input.Targets`, and scope
decisions read `ShellTarget.Scope`. They are vestiges of an earlier design in
which dispatch supplied targets. Removing them is mechanical but spans the
native tools and the admission path, so it is left as a follow-up rather than
mixed into the review fixes.
