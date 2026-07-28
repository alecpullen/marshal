package routing

// SingleModelProfile builds a profile binding every chat role to one
// preset. It is how "I just want one model" is expressed: the router only
// ever resolves through profiles, so a single-model setup is a profile the
// user never has to look at rather than a second resolution mechanism.
//
// RoleEmbedding is excluded deliberately — it is not in AllRoles, and a
// chat preset cannot serve it.
func SingleModelProfile(name, presetName string) AgentProfile {
	roles := make(map[AgentRole]RoleBinding, len(AllRoles))
	for _, role := range AllRoles {
		roles[role] = RoleBinding{Preset: presetName}
	}
	return AgentProfile{Name: name, Roles: roles}
}
