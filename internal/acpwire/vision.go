package acpwire

const MethodVisionForce = "session/vision_force"

// VisionForceParams records the user's send-anyway answer after a vision
// refusal: the provider and model the prompt was refused on.
type VisionForceParams struct {
	SessionID string `json:"sessionId"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
}
