package routing

// RoleDefaultTemperature returns the built-in sampling temperature for roles
// whose output must be deterministic: implementers and testers/reviewers.
// Nil for every other role — planning, scouting, and chat keep the provider's
// own default. Applied only when the preset itself does not set a temperature.
func RoleDefaultTemperature(role AgentRole) *float64 {
	switch role {
	case RoleImplementer, RoleSDDImplementer:
		return floatPtr(0.2)
	case RoleReviewer, RoleSecurityReviewer, RoleSDDReviewer, RoleSDDBranchReviewer, RoleTester:
		return floatPtr(0.1)
	}
	return nil
}

func floatPtr(v float64) *float64 { return &v }
