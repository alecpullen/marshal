package schema

import (
	"context"
	"errors"
	"fmt"
)

// ImageRef identifies an image in the payload store. It carries no bytes:
// encoders resolve them through ChatRequest.ImageSource at send time, so
// transcripts, ACP frames and the cached prompt prefix stay small.
type ImageRef struct {
	Hash   string `json:"hash"`            // content hash; the store key
	MIME   string `json:"mime"`            // "image/png" | "image/jpeg"
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`           // stored size, not source size
	Label  string `json:"label,omitempty"` // "screenshot of /tmp/ui.png"
}

// ImageSource resolves an image ref to what an encoder puts on the wire. A
// nil ImageSource on a request means the request cannot carry images: an
// encoder or estimator meeting non-empty Images with a nil source fails with
// ErrVisionUnsupported rather than silently sending text only.
type ImageSource interface {
	Image(ctx context.Context, hash string) (data []byte, mime string, err error)
}

// ErrVisionUnsupported reports that images were supplied to a path that
// cannot carry them. Callers classify with errors.Is.
var ErrVisionUnsupported = errors.New("images are not supported on this route")

// VisionError is a refusal at the prompt boundary. It names the cause so the
// TUI and ACP can offer the fix (one config line, or the "send anyway" key).
type VisionError struct {
	Model  string
	Reason string // VisionReasonPresetOff | VisionReasonRejected
	Detail string // e.g. "rejected image input on 2026-11-02"
}

// VisionError reasons.
const (
	VisionReasonPresetOff = "preset_off"
	VisionReasonRejected  = "rejected"
)

func (e *VisionError) Error() string {
	return fmt.Sprintf("model %s cannot take images: %s", e.Model, e.Detail)
}

// Is reports the refusal as ErrVisionUnsupported so callers classify one way.
func (e *VisionError) Is(target error) bool { return target == ErrVisionUnsupported }
