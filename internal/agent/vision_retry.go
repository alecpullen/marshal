package agent

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/schema"
)

// isVisionRejection reports whether err is a provider response that refuses
// image input. It is deliberately narrow: a 400 about request size or an
// unknown parameter must never be read as "this model is blind".
func isVisionRejection(err error) bool {
	var pe *provider.ProviderError
	if !errors.As(err, &pe) {
		return false
	}
	switch pe.StatusCode {
	case http.StatusBadRequest, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
	default:
		return false
	}
	body := strings.ToLower(pe.Body)
	for _, s := range []string{"image", "vision", "multimodal", "image_url", "does not support images"} {
		if strings.Contains(body, s) {
			return true
		}
	}
	return false
}

// withoutImages returns a copy of msgs with every Images list cleared and a
// notice appended to the last message that carried any.
func withoutImages(msgs []schema.ChatMessage) ([]schema.ChatMessage, bool) {
	out := append([]schema.ChatMessage(nil), msgs...)
	last := -1
	for i := range out {
		if len(out[i].Images) > 0 {
			out[i].Images = nil
			last = i
		}
	}
	if last < 0 {
		return msgs, false
	}
	out[last].Content += "\n[images omitted: model rejected image input]"
	return out, true
}

// carriesImages reports whether any message in msgs still holds an image.
func carriesImages(msgs []schema.ChatMessage) bool {
	for _, m := range msgs {
		if len(m.Images) > 0 {
			return true
		}
	}
	return false
}

// recordVisionRejection persists "this provider/model refuses images".
// Nil-safe: tests and headless paths wire nothing and learn nothing. The
// provider config hash is what makes a repointed endpoint invalidate the
// learned fact; both sides of the comparison come from
// modelcache.HashProvider, so the router's read and this write agree.
func (r *Runner) recordVisionRejection(providerName, model string) {
	if r.VisionOverrides == nil || r.SaveVisionOverrides == nil {
		return
	}
	if r.VisionOverrides.Rejections == nil {
		r.VisionOverrides.Rejections = map[string]modelcache.Rejection{}
	}
	configHash := ""
	if r.ProviderConfigHash != nil {
		configHash = r.ProviderConfigHash(providerName)
	}
	r.VisionOverrides.Rejections[providerName+"/"+model] = modelcache.Rejection{
		ObservedAt: time.Now().UTC(),
		ConfigHash: configHash,
	}
	_ = r.SaveVisionOverrides(*r.VisionOverrides)
}

// noteVisionRetry tells the user, in the transcript, that this turn went
// out without its images. Silence here would read as the model ignoring the
// screenshot.
func (r *Runner) noteVisionRetry() {
	r.State.AddMessage(session.RoleSystem,
		"the model rejected image input; resent without images",
		session.ContentTypeCompaction)
}
