package provider

import (
	"context"
	"encoding/base64"
	"fmt"

	"marshal/internal/llm/schema"
)

// imageDataURL resolves one ref and formats it as an OpenAI-style data URL.
// A message that carries images on a request with no ImageSource is a
// programming error at the send boundary, not something to paper over: the
// model would otherwise be asked about an image it never received.
func imageDataURL(ctx context.Context, src schema.ImageSource, ref schema.ImageRef) (string, error) {
	if src == nil {
		return "", fmt.Errorf("%w: %s", schema.ErrVisionUnsupported, ref.Label)
	}
	data, mime, err := src.Image(ctx, ref.Hash)
	if err != nil {
		return "", fmt.Errorf("resolve image %s: %w", ref.Hash, err)
	}
	if mime == "" {
		mime = ref.MIME
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func imageB64(ctx context.Context, src schema.ImageSource, ref schema.ImageRef) (string, error) {
	if src == nil {
		return "", fmt.Errorf("%w: %s", schema.ErrVisionUnsupported, ref.Label)
	}
	data, _, err := src.Image(ctx, ref.Hash)
	if err != nil {
		return "", fmt.Errorf("resolve image %s: %w", ref.Hash, err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}
