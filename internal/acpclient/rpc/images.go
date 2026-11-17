package rpc

import (
	"context"
	"encoding/base64"

	"marshal/internal/acpwire"
)

// ImagesPut uploads one image and returns the ref a later prompt attaches.
// The bytes travel once, here; the prompt carries only the ref.
func (s *Session) ImagesPut(ctx context.Context, sessionID string, data []byte, label string) (acpwire.PromptImage, error) {
	if sessionID == "" {
		sessionID = s.id
	}
	params := acpwire.ImagesPutParams{
		SessionID: sessionID,
		Data:      base64.StdEncoding.EncodeToString(data),
		Label:     label,
	}
	var result acpwire.ImagesPutResult
	if err := s.conn.Call(ctx, acpwire.MethodImagesPut, params, &result); err != nil {
		return acpwire.PromptImage{}, err
	}
	return result.Image, nil
}
