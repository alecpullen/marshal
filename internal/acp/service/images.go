package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"

	"marshal/internal/acpwire"
	"marshal/internal/imagepayload"
	"marshal/internal/llm/schema"
)

// errNoImages is what a prompt or an upload carrying images answers on a
// session whose payload store is not wired.
var errNoImages = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no image store"}

// errImageRunner is the refusal of a prompt carrying images on a runner that
// has no image-carrying run. It deliberately does not offer the vision
// setting as the fix: a runner that cannot carry images is not made able by
// turning vision on, and naming that setting would send the user to a change
// that does nothing.
var errImageRunner = &acpwire.Error{
	Code:    acpwire.CodeVisionUnsupported,
	Message: "this session's runner cannot carry images",
}

// PutImage stores the image a client uploaded and returns its ref, which the
// client attaches to a later prompt.
func (s *Session) PutImage(ctx context.Context, p acpwire.ImagesPutParams) (acpwire.ImagesPutResult, error) {
	if s.closed.Load() {
		return acpwire.ImagesPutResult{}, errSessionClosed
	}
	if s.images == nil {
		return acpwire.ImagesPutResult{}, errNoImages
	}
	data, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil {
		return acpwire.ImagesPutResult{}, fmt.Errorf("image data is not base64: %w", err)
	}
	ref, err := s.images.Put(ctx, bytes.NewReader(data), imagepayload.Options{Label: p.Label})
	if err != nil {
		return acpwire.ImagesPutResult{}, err
	}
	return acpwire.ImagesPutResult{Image: promptImageOf(ref)}, nil
}

// ImagesPut uploads one image and returns its ref, as the client seam's method
// names it: the in-process session stores it through PutImage.
func (s *Session) ImagesPut(ctx context.Context, sessionID string, data []byte, label string) (acpwire.PromptImage, error) {
	res, err := s.PutImage(ctx, acpwire.ImagesPutParams{
		SessionID: sessionID,
		Data:      base64.StdEncoding.EncodeToString(data),
		Label:     label,
	})
	if err != nil {
		return acpwire.PromptImage{}, err
	}
	return res.Image, nil
}

// promptImages resolves a prompt's image refs. A ref the store does not hold
// fails the prompt before the turn starts: the store, not the route, is what
// cannot answer, so the caller gets a plain error rather than a vision refusal.
// A session with no store refuses as it does for any unwired dependency.
func (s *Session) promptImages(ctx context.Context, in []acpwire.PromptImage) ([]schema.ImageRef, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if s.images == nil {
		return nil, errNoImages
	}
	out := make([]schema.ImageRef, 0, len(in))
	for _, p := range in {
		stored, ok, err := s.images.Stat(ctx, p.Hash)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("image %s is not in the store", p.Hash)
		}
		// The store is the truth about the bytes. A client ref can name a
		// hash while understating its dimensions, and the token estimator
		// reads them; the client's label is the one thing worth keeping,
		// because that is the user's own name for the picture.
		if p.Label != "" {
			stored.Label = p.Label
		}
		out = append(out, stored)
	}
	return out, nil
}

// promptImageOf is a stored ref as the wire carries it.
func promptImageOf(r schema.ImageRef) acpwire.PromptImage {
	return acpwire.PromptImage{
		Hash:   r.Hash,
		MIME:   r.MIME,
		Width:  r.Width,
		Height: r.Height,
		Bytes:  r.Bytes,
		Label:  r.Label,
	}
}

// imageRefOf is a wire ref as the session and the encoders read it. The store
// already verified the hash and its own sidecar is the truth about the bytes,
// so the client's copy of the rest is what the turn carries.
func imageRefOf(p acpwire.PromptImage) schema.ImageRef {
	return schema.ImageRef{
		Hash:   p.Hash,
		MIME:   p.MIME,
		Width:  p.Width,
		Height: p.Height,
		Bytes:  p.Bytes,
		Label:  p.Label,
	}
}
