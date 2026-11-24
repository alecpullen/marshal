# Shared skill discovery

Marshal discovers skills in the agent user's `~/.agents/skills`, including
symlinked skill bundles. A missing directory is empty. Shared files are managed
by their installer. Marshal's skill manager lists and loads them.

Default lookup is project, Marshal global, built-in, then shared. The user can
swap built-in and shared for specific declared skill names with
`[skills].prefer_shared` in the user config. Missing or invalid shared copies
fall back to built-ins. Project and Marshal-global copies retain priority.

In `/skills`, users can see each copy's source and which copy wins. When a name
has both a built-in and a shared copy, the panel offers **Prefer shared copy**
or **Use built-in copy**. The choice persists in user config and takes effect
for future loads immediately. An active skill keeps its current instructions
until unloaded and loaded again.

We chose automatic discovery to make skills installed by other agents
available without setup. We chose a panel action alongside TOML so users can
inspect and change a preference in the same place.
