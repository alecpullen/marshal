# New project from the Studio

Date: 2026-11-25
Status: implemented on `feat/studio-new-project`.

## Why

A project could only appear on a host by someone running `marshal` in a
directory there: route-parity dropped `POST /api/projects` with "projects are
discovered by the daemon, never created from the app", and the plus beside
*Projects* opened *Connect a repository*, which registers a repository for
issue intake and creates no project. A Studio on another machine had no way to
start work in a new codebase.

This reverses that route-parity entry. A project is still a directory on a host
that the daemon registers; the Studio now asks the host to make one.

## Decisions

1. **Three ways in, one sheet.** *Blank* makes an empty git repository, *Clone*
   clones a URL, and *Existing folder* registers a directory already on the
   host (a repository cloned over SSH by hand, say). All three end the same
   way: the root registered, as `marshal` in that directory would.
2. **A parent folder and a name.** The Studio usually runs on another machine
   and cannot browse the host's disk, so each host has a projects folder,
   `[daemon] projects_dir`, default `~/projects`, and the sheet asks for a
   name. For a clone the name follows the URL until it is edited. The full
   path is shown and editable, so a project can live anywhere the bridge
   accepts a root (under home, the temp directory or its working directory,
   as `ValidateProjectRoot` already rules).
3. **A clone uses the host's own git.** The daemon runs `git clone` as its
   user, with that user's global config, credential helpers, SSH keys and
   agent, as cloning in a terminal there would. Unlike the mirror clones
   (`gitEnv`), the ambient config is kept: the URL is the owner's, typed by the
   owner, and their helpers are how private repositories are reached. Hooks
   and the `ext` transport stay off (`hardenedGitArgs`), prompts stay off
   (`GIT_TERMINAL_PROMPT=0`), and the URL follows `--`, so a URL can never be
   read as an option. A clone may instead name a stored credential
   (`credRef`), and then runs exactly as a mirror clone does, with the ambient
   config cut off.
4. **A clone runs in the background.** It can take minutes, longer than a
   proxy keeps a request open, so `POST` answers `202` with a creation id
   while git runs, and the sheet reads the creation until it is ready or
   failed, showing git's latest progress line. Closing the sheet does not stop
   it. A failed clone removes the directory it made, and only that: a
   directory that existed (empty) before is left. Creations are kept in memory
   — a daemon restart forgets an in-flight one, and git's partial directory
   then stays for the owner to remove or reuse.
5. **A blank project is `git init`, with no commit.** A first commit needs an
   author the host may not have configured; the first change made in the
   project is its first commit. `git init` uses the host's git, so
   `init.defaultBranch` applies.
6. **Blank and Clone need an absent or empty directory; Existing needs a
   directory.** Nothing is overwritten. Two creations of one root at once are
   refused (`409`).
7. **The project list is pushed.** Registering a root emits a `project_added`
   delta, so every open Studio reads the host's projects again, rather than
   waiting for its next snapshot.
8. **The plus beside *Projects* opens this sheet**, titled *Add a project*;
   *Connect a repository* stays in Settings → Connections, where its issue
   intake belongs. The conversation entry with no project offers *Add a
   project* too.

## Contract

`GET /api/projects/new` → `NewProjectOptions`: the host's `projectsDir`, with
`~` resolved, and the creations it remembers, newest first.

`POST /api/projects/new` with `NewProjectRequest`:

| field | |
|---|---|
| `kind` | `blank`, `clone` or `existing` |
| `root` | absolute path of the project directory |
| `url` | clone only: what `git clone` is given |
| `branch` | clone only, optional: `--branch` |
| `credRef` | clone only, optional: a stored credential |

It answers `ProjectCreation` — `id`, `kind`, `root`, `url`, `state`
(`cloning`, `ready` or `failed`), `progress` (git's last progress line),
`error`, `startedAt` — with `200` when the project is registered already
(blank, existing) and `202` while a clone runs. A refusal is `400` with the
bridge's reason, `409` for a root already being created.

`GET /api/projects/new/{id}` → that `ProjectCreation`, `404` once forgotten.

`project_added` delta: `{kind, project}`.

All three routes are on the mux, so the web listener and a host forward serve
them as they serve `POST /api/projects`.

## Not in this

- Templates or scaffolding for a blank project.
- Creating the remote repository on a forge.
- Registering the clone's repository for issue intake: that stays *Connect a
  repository*.
- Cancelling a clone in flight.
