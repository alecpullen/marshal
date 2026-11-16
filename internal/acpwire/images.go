package acpwire

const MethodImagesPut = "session/images_put"

// PromptImage carries a ref across the wire. It duplicates schema.ImageRef's
// fields because acpwire is stdlib-only and converts at the seam.
type PromptImage struct {
	Hash   string `json:"hash"`
	MIME   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
	Label  string `json:"label,omitempty"`
}

type ImagesPutParams struct {
	SessionID string `json:"sessionId"`
	Data      string `json:"data"` // base64
	Label     string `json:"label,omitempty"`
}

type ImagesPutResult struct {
	Image PromptImage `json:"image"`
}
