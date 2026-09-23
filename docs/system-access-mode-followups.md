# System Access Mode — Deferred Follow-Ups

Items identified during branch review of `system-access-mode`
(base `6ac9bf73`, head `5b00d677`) and deliberately deferred from the
merge. Each is documented here so it can be tracked and addressed in
subsequent work.

The branch was merged with these items open. The two blocking findings
from the first review round (the catastrophic floor failing open, and
backup rollback restoring out-of-root writes to the wrong path) were
fixed before merge; what follows is the residue from the second,
adversarial round.

## 1. The non-bypassable git-push floor is bypassable by wrapper and payload shapes

**Status: open. Severity: critical (system access + auto mode), high (flag off + auto mode).**

The push floor is documented and tested as non-bypassable in every mode:
it runs before `applyModeTransform` (`internal/tools/policy/policy.go:253`),
so a push it catches lands on `DecisionConfirm` even in auto mode. That
contract does not hold. Every shape below really executes a push and was
observed to reach **Allow / "auto-approved in auto mode"** under
`Evaluate("shell.run", {"command": C}, WithSystem(true))` in `ModeAuto`:

| Shape | Example |
|---|---|
| `exec` prefix | `exec git push` |
| `command` prefix | `command git push` |
| inline shell payload | `sh -c 'git push'`, `bash -c 'git push'`, `bash -lc 'git push'`, `bash --login -c 'git push'`, `sh -c 'sh -c "git push"'` |
| privilege + inline payload | `sudo sh -c 'git push'` |
| `su` payload | `su -c 'git push'`, `su root -c 'git push'`, `sudo su -c 'git push'` |
| `eval` | `eval 'git push'`, `eval git push` |
| process wrappers | `nohup git push`, `timeout 30 git push`, `setsid git push`, `watch -n 10 git push` |
| `xargs` payload | `echo \| xargs git push`, `echo \| xargs -I{} git push` |

`builtin git push` also slips past the floor, but is not exploitable:
`builtin` only runs shell builtins, so git never executes. It is listed
for completeness of the wrapper-blindness diagnosis.

### Root cause

`isGitPushFloorWithSystem` (`internal/tools/policy/policy.go:1065`) strips
only `env`/`nice` (via `skipCommonWrappers`, `policy.go:1120`) plus `sudo`
(via `skipFloorWrappers`, `policy.go:1170`), and then inspects only the
top-level stage argv through `gitPushInArgv` (`policy.go:1093`). It never
recurses into inline shell payloads, and it knows nothing of `exec`,
`command`, `eval`, `nohup`, `timeout`, `setsid`, `watch`, `xargs`, or `su`.
Each shape therefore falls through the floor, and the resulting Confirm is
then auto-approved by `applyModeTransform`.

The guardrail analyzer already has the machinery this floor needs:
`shellInlinePayload` (`policy.go:896`) extracts a `sh -c`/`bash -c` payload
and `maxGuardrailDepth` (`policy.go:661`) bounds the recursion. The push
floor simply does not use it.

### Why it matters

Under system access the `sudo` substring guardrail is intentionally
dropped, so `sudo sh -c 'git push'` and `su`/`sudo su -c 'git push'`
allow **unattended, privilege-escalated real pushes** in auto mode. The
same shapes bypass the floor with the flag off, where auto mode also
auto-approves them; in edit mode a human still gates them via the generic
secure-config Confirm, which is why the flag-off case is high rather than
critical.

### Suggested fix (not applied)

Make `isGitPushFloorWithSystem` reuse the guardrail payload machinery:

1. Extend the wrapper stripper to also strip `command`, `builtin`,
   `exec`, `nohup`, `setsid`, `timeout <n>`, `watch`, and `xargs` payloads.
2. Add a `suInlinePayload` sibling of `shellInlinePayload` for
   `su [-user] -c <payload>`.
3. Recursively run `gitPushInArgv` over `shellInlinePayload` results,
   bounded by the same `maxGuardrailDepth` used by `analyzeCommandDepth`.
4. Treat `eval <args>` by joining argv and re-parsing.
5. Keep `sudo` stripping system-only, so flag-off `sudo git push` stays a
   guardrail Deny rather than being downgraded to the floor's Confirm.
6. Extend payload recursion to the flag-off path too — flag-off
   `exec git push` and `sh -c 'git push'` bypass the floor today.

Because the floor runs before `applyModeTransform`, catching these shapes
yields Confirm in every mode.

### Test coverage gap

The committed push-floor table in `internal/tools/policy/system_floor_test.go`
(lines 24-32) covers only plain, `sudo`-prefixed, path-prefixed, and
`env`-prefixed pushes. It has no `sh -c` payload case, no
`exec`/`command`/`eval`/`nohup`/`timeout`/`setsid`/`watch`/`xargs`/`su`
case, and no nested-payload case — exactly the shapes that leak. Any fix
should add the table above as table-driven regression cases.

## 2. Pre-existing: the same bypasses work with the flag off

**Status: open. Severity: high. Not a regression from this branch.**

With system access off, in `ModeAuto`, these all reach Allow:
`exec git push`, `command git push`, `sh -c 'git push'`,
`bash -c 'git push'`, `bash -lc 'git push'`, `nohup git push`,
`timeout 30 git push`, `eval 'git push'`, `echo | xargs git push`,
`watch -n 10 git push`, `setsid git push`.

The floor's "non-bypassable in every mode" contract is therefore falsified
independently of the system-access flag. In `ModeEdit` these land on the
generic secure-config Confirm, so a human still gates them. This is the
same root cause as item 1 and should be fixed in the same pass.

## 3. What was verified to hold

Recorded so a future fix does not regress them.

- **Fail-closed catastrophic floor holds.** All 12 spot-checks Deny with
  guardrail reasons: `rm -rf /etc`, `rm -rf "/etc"`,
  `rm -rf /etc ./notes`, `sudo rm -rf /etc`, `find / | xargs rm -rf`,
  `xargs -0 rm -rf < /tmp/list`, `rm -rf $HOME/x`, `rm -rf ~`,
  `sh -c 'rm -rf /etc'`, `sudo chmod -R 000 /etc`,
  `sudo find / -delete`, `sudo dd if=/dev/zero of=/dev/sda`.
- **Expected Allows hold.** `rm -rf rel/x`, `sudo rm -rf rel/x`,
  `sudo ls`, `sudo find / -name x`.
- **No false positives.** Every Confirm verdict in system+auto was a real
  push. `sudo git pushd` correctly lands Allow in system+auto (not a push)
  and Deny flag-off (sudo guardrail).
- **The round-one fixes hold.** Flag-off `sudo git push` is a sudo-guardrail
  Deny again; system-access `sudo git push` is a floor Confirm, not Allow.
  All 15 privilege-prefixed push variants (`sudo -n`, `sudo -u root`,
  `env sudo`, `nice sudo`, `sudo env`, `/usr/bin/sudo`, `sudo /usr/bin/git`,
  `sudo git -c a=b push`, `sudo git push --force`, …) are Confirm with the
  floor reason under system access.

## 4. Verification evidence for this document

All commands were run in the worktree at `5b00d677`; the main checkout was
left untouched at `6ac9bf73`. Probe files were created, run, and deleted;
`git status --short` was empty afterwards.

- `CGO_ENABLED=1 go test ./internal/tools/policy/... -count=1` →
  `ok marshal/internal/tools/policy`
- Probe runs (`-run 'TestProbeAudit' -v -count=1`) produced the per-command
  verdicts quoted above, e.g.
  `PROBE sys-auto "sudo sh -c 'git push'" = allow | auto-approved in auto mode`
  and
  `PROBE off-edit "sudo git push" = deny | blocked by conservative guardrail: sudo`.
- The extra-shapes table was re-run for determinism with identical verdicts.

## References

- Spec: `.docs-archive/superpowers/specs/2026-09-23-system-access-mode-design.md`
- Plan: `.docs-archive/superpowers/plans/2026-09-23-system-access-mode-plan.md`
- Floor implementation: `internal/tools/policy/policy.go`
- Floor tests: `internal/tools/policy/system_floor_test.go`,
  `internal/tools/policy/gitpush_floor_test.go`
