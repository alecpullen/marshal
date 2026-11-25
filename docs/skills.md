# Skills

A skill is a Markdown file — or a bundle directory — whose frontmatter declares
a name and a description and whose body is the instructions the agent loads
into the conversation when it needs them. Marshal discovers skills from four
sources, picks one copy per declared name, and lets you say which of the last
two you want to run. `/skills` shows the copies, the winner and the preference,
and is the only place Marshal writes it.

## Automatic discovery

Every session builds its skill index at startup from four sources:

| Source | Where it lives | Writable through Marshal |
|---|---|---|
| `project` | `<repository>/.marshal/skills/` | yes (a trusted folder) |
| `global` | the skills directory under Marshal's config dir, `~/.config/marshal/skills/` by default | yes |
| `builtin` | embedded in the binary | no — part of Marshal |
| `shared` | `~/.agents/skills/` | no — another agent installs there |

`MARSHAL_CONFIG_DIR` and `XDG_CONFIG_HOME` relocate the config dir, so the
global store moves with it (`<config dir>/skills/`). The project store anchors
at the repository root, not at the directory Marshal was launched from.

A source directory may hold either shape, indifferently:

- a flat `<name>.md` skill file;
- a bundle directory containing `SKILL.md`, which is how other agents ship a
  skill with supporting files.

The **declared name in the frontmatter is content, not identity**. Identity is
the copy's source plus its name inside that source (the file name, or the
bundle directory's name). Two copies can declare the same name — that is what
the precedence below resolves — and two entries in one store can even share
one. When they do, the store's own listing order decides, and a directory reads
in name order, so within one source the first entry in that order wins and the
same tree always resolves the same way. A broken or unreadable file is skipped
rather than failing the source, and the declared name a skill advertises is
revalidated before anything acts on it.

The shared root is treated as another tool's directory: a missing root, an
unreadable entry, or a dangling link is empty for discovery, so Marshal keeps
working without it and every existing skill stays available. Marshal only ever
reads from it.

## Precedence

When more than one copy declares the same name, the winner is decided by
source, highest priority first:

1. `project`
2. `global`
3. `builtin`
4. `shared`

The default order means an explicit install wins, and another agent's shared
copy never silently replaces a built-in. Plugin skills are registered after
discovery and can shadow the resolved value for a name.

The saved preference swaps **only the last two**: a name in
`skills.prefer_shared` runs its shared copy instead of the built-in one, while
a project or a Marshal-global copy of the same name still outranks both. That
is deliberate — the preference is a choice between Marshal's own copy and
another tool's, never over the repository's or your own install. A higher
priority copy that masks the preference is named in `/skills`, so a preference
that is saved but not in effect never looks like it took effect.

## The TOML list

The preference is one user-global key in the user config
(`~/.config/marshal/config.toml`, or the config dir named above):

```toml
[skills]
prefer_shared = ["brainstorming"]
```

Each entry is a **declared skill name**, not a source name or file name. The
key is user-global only: which copy of a skill Marshal runs is your choice as
a person, not a property of the repository, so a project
`.marshal/config.toml` that sets it is ignored, and `/doctor` reports that it
was (`skills.prefer_shared is user-global only; the project config's value is
ignored`). `/skills` writes this key for you and leaves the rest of the file
alone.

Unlike `[providers]` and `[models.presets]`, which are migrated into your user
config, this key is never hoisted: a project file's value is ignored where it
stands and stays there, so a repository never gets to pick which copy you run.

A name whose preferred shared copy is invalid, broken or missing simply falls
back to the built-in copy — the preference is a preference, not a requirement,
and a shared root that disappears never takes a skill with it.

## The `/skills` panel

`/skills` lists every copy of every skill across the four sources. Each row's
summary carries the copy's source, then whether it autoloads (`autoload`),
whether it is the copy the session resolves for its name (`effective`), and
whether the saved preference names the skill (`shared preferred`).

Opening a copy shows:

- **Scope** — the source the copy came from.
- **Effective** — whether this copy is what the session loads, and when it is
  not, which copy is.
- **Preference** — `built-in copy (default)` until the name is preferred, then
  `shared copy preferred`; when a project or global copy outranks the saved
  preference, the row says which copy takes precedence.
- **Autoload** — whether the skill is injected into every session at startup.
- **Load now** — inject this copy into the running session, when the session's
  index actually holds the same content.
- **Prefer shared copy** / **Use built-in copy** — offered only when the
  declared name has both a built-in and a shared copy, i.e. when there is a
  pair to swap. The action saves to the user config through the agent side;
  the panel itself holds no store paths and writes nothing.
- **Remove** — only for the two managed stores. A built-in has no file to
  delete, and the shared root belongs to whichever agent installed there, so
  those copies say why instead of offering a deletion Marshal would refuse.

Installing is likewise restricted to the two managed stores — `global` and,
on a trusted folder, `project`. There is no way to install into the built-in
or shared sources.

Two consequences of how the runtime works are worth knowing:

- The index is built at startup. A changed file, an install, a removal or a
  new preference changes what the **next** session resolves; the panel says so
  after each action, and the running session keeps the value it started with.
- An active skill keeps the body it was loaded with until it is unloaded and
  loaded again. Preferring the other copy is not a reload.
