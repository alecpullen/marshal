package acpwire

// TrustDecision answers the folder-trust prompt (C10, plan 7c).
type TrustDecision string

const (
	TrustPermanent TrustDecision = "trust_permanent"
	TrustSession   TrustDecision = "trust_session"
	TrustDecline   TrustDecision = "dont_trust"
)
