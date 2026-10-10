# System Access Mode — Privileged Execution (`shell.sudo`)

System access mode lets Marshal's file tools read and write absolute paths
anywhere on the filesystem and shrinks shell guardrails to a catastrophic
floor. It does **not** grant root. Privileged work goes through one tool,
`shell.sudo`, and every privileged command re-enters `sudo` with a password
you type into a masked prompt.

Design: `.docs-archive/superpowers/specs/2026-10-10-system-mode-sudo-design.md`

## The contract

- **`sudo` is not a shell concept any more.** Under system access, any
  `shell.run` command that invokes `sudo`/`su`/`doas`/`pkexec`/`run0` — at
  any depth, through any wrapper (`sh -c`, `eval`, `nohup`, `xargs`,
  `timeout`, `env`, …) — is **Deny**, with a reason naming `shell.sudo`.
  This is deliberate: it closes the escalation shapes recorded in
  `docs/system-access-mode-followups.md` item 1.
- **The only path to root is `shell.sudo`.** It is a deferred tool
  (`tools.select` discloses its full contract on demand) and is registered
  only on non-Windows platforms.
- **Two gates.** The tool refuses unless the session has system access, and
  the command itself is validated (`CheckPrivileged`) to reject a command
  that invokes a privilege wrapper — you pass the *bare* command; Marshal
  adds the leading `sudo`.
- **TUI only (v1).** ACP/web clients have no secret channel, so a
  privileged call from one is refused cleanly rather than blocking on a
  prompt no transport can answer. `State.SecretPromptCapable` is the hook a
  future `session/request_secret` method will flip.

## What the agent passes

| Field | Notes |
|---|---|
| `command` | The bare command, e.g. `systemctl restart nginx`. No leading `sudo`. |
| `reason` | Optional; shown in the prompt. |
| `timeout_seconds` | Default 300, max 900. |

There is deliberately **no** `background`, no `env`, no `cwd`, and no
`-u`/`-i`/`-s`/`-e` target-user selection.

## How the password is handled

The password is delivered to sudo over **stdin**, never argv and never the
environment:

```
sudo [-k] -S -p <empty> -- bash -o pipefail -c <command>
```

- `-S` reads the password from stdin; `-p` with an empty argument
  suppresses sudo's own prompt text; `--` stops option parsing so a command
  starting with `-` cannot become a sudo flag.
- The command is passed as a **single argv element** to the shell, so there
  is no argv-injection path.
- **No PTY is ever allocated.** sudo cannot echo the password and the
  terminal cannot capture it. This is also why `requiretty` is not worked
  around.
- The password is **never stored, never logged, never placed in argv**, and
  never travels on a session `Event` — the published snapshot carries only
  the response channel.
- The `[]byte` copy is zeroed immediately after the command returns.

### Honest limitations

- **The TUI's `string` copy of the typed text cannot be zeroed.** Go strings
  are immutable. That copy is never logged or stored and is released to the
  GC when the prompt closes, but it is best-effort only.
- **Interactive privileged commands see EOF on stdin**, because stdin
  carries the password and is then closed. Pass non-interactive flags
  (`apt-get -y`, `--non-interactive`) rather than relying on a prompt.
- **`LookPath` resolves whatever `PATH` says.** A hostile `PATH` is not
  defended against.
- **An unread password can sit in the kernel pipe buffer** if sudo never
  reads stdin (NOPASSWD, or a warm unlock grant). It is closed immediately
  and freed when the child exits; it is not on disk and not zeroable from
  userspace.

## Fresh vs. unlock

**Fresh (default).** Marshal passes `-k`, which invalidates any cached sudo
grant, so **every** privileged command prompts. Nothing Marshal holds short
of your password is sufficient to gain root.

**Unlock (opt-in).** `/system unlock` toggles it. Marshal omits `-k` and
relies on **sudo's own timestamp record** — there is no resident privileged
process and no long-lived privileged shell. On the unlock path the command
is tried *first* with an empty stdin: a warm grant means sudo never reads
stdin and the command runs (one process, no probe, no race); a cold grant
means sudo reads EOF and reports that it needed a password, and only then
does Marshal prompt. If the record cannot persist, every call simply
prompts — graceful degradation to fresh mode.

The grant lives in sudo's record (`/run/sudo/ts/<user>`, root-owned), keyed
by user plus a session discriminator (the tty when there is one, otherwise
the parent PID). It dies with Marshal restarting, with `sudo -k`, and with
`timestamp_timeout`.

### sudoers knobs

```
# How long the unlock window lasts (minutes; 0 = always prompt).
Defaults timestamp_timeout=15

# Headless / ssh boxes: sudo otherwise refuses with "a terminal is required".
Defaults !requiretty
```

**`timestamp_type` caveat.** If sudoers sets `timestamp_type=tty`, the
record is keyed to a terminal — and Marshal's privileged child has no tty,
so the record cannot persist between commands. Unlock then degrades to
fresh mode (every command prompts). That is the safe direction, but it
means unlock may appear to do nothing on such a box; check
`sudo -V | grep -i timestamp` if so.

## Failure modes

| Situation | Behaviour |
|---|---|
| Wrong password | Prompt re-raised with "incorrect password", up to 3 attempts, then the call fails. Never loops unattended. |
| `sudo` not on PATH | Tool error before any prompt. |
| `requiretty` set | Tool error recommending `Defaults !requiretty`. No PTY is allocated. |
| NOPASSWD | Not detected — the command just runs. Marshal never sniffs sudoers to skip the gate. |
| You press Esc | Deny. Nothing executes. Esc always denies. |
| Turn cancelled / shutdown | The prompt is denied and cleared; no password is buffered. |
| Second concurrent call | Refused with a clean "retry" error — privileged commands are serialized session-wide. |
| Timeout / interrupt | The whole privileged process group is killed, sudo included. |

## Kill switch

The entire feature is gated on system access. `/system` off disables
`shell.sudo` and restores `shell.run`'s flag-off behaviour in one step.
