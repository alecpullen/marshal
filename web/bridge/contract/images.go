package contract

type PromptImage struct {
	Hash   string `json:"hash"`
	MIME   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
	Label  string `json:"label,omitempty"`
}

type ImageUpload struct {
	Data  string `json:"data"` // base64
	Label string `json:"label,omitempty"`
}

type ImageUploadResult struct {
	Image PromptImage `json:"image"`
}
