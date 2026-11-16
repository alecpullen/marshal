package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// isVisionRejection reports whether err is a provider response that refuses
// image input. It is deliberately narrow: a 400 about request size or an
// unknown parameter must never be read as "this model is blind".
//
// The wording match is phrase-based on purpose. A bare "image" is not a
// refusal — "invalid image field", "image pipeline error", "image must be
// smaller" all contain it — and the verdict is persisted for the capability
// TTL, so a false positive takes images off that model for a month. Callers
// only reach here for a request that carried images, which is what lets 415
// stand on the status alone: an unsupported media type refuses the media
// itself, and the only non-text media a Marshal request carries are images.
func isVisionRejection(err error) bool {
	var pe *provider.ProviderError
	if !errors.As(err, &pe) {
		return false
	}
	switch pe.StatusCode {
	case http.StatusUnsupportedMediaType:
		return true
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
	default:
		return false
	}
	body := strings.ToLower(pe.Body)
	for _, s := range []string{"image_url", "image content", "image input", "does not support images", "multimodal", "vision"} {
		if strings.Contains(body, s) {
			return true
		}
	}
	return false
}

// visionRefusal answers "may this turn carry these images" for a resolved
// route, returning the refusal that names the model and the fix. Nil means
// the turn may proceed. It is one function so the cause and the exemption
// rules cannot drift between the refusal and the retry gate that follows it.
func (r *Runner) visionRefusal(route routing.Route) *schema.VisionError {
	if route.Vision || route.VisionForced {
		return nil
	}
	// The session's send-anyway answer for this model is the user's word
	// too: they were already told the model looks blind and chose to try.
	if r.State != nil && r.State.VisionForced(route.Preset.Provider, route.Preset.Model) {
		return nil
	}
	model := route.Preset.Model
	if model == "" {
		// A route that resolved no model has no cause to name, and the
		// wording below would read "model  has vision off" — no model, no
		// fix, no clue. Leave the refusal to whatever the route itself
		// reports.
		return nil
	}
	reason, detail := schema.VisionReasonPresetOff,
		fmt.Sprintf("model %s has vision off; set [models.presets.%s] vision = \"on\" or switch model", model, model)
	// A learned rejection is not the preset's doing. Naming the preset would
	// send the user to change a setting that was never the cause, and
	// switching that preset on would just reproduce the rejection. Only a
	// route whose vision-off the preset does not itself explain can have
	// been made vision-off by the learned record.
	if route.Preset.Vision != routing.VisionOff {
		if observedAt, ok := r.visionRejectedSince(route.Preset); ok {
			reason = schema.VisionReasonRejected
			detail = fmt.Sprintf("model %s rejected image input on %s; switch model, or send anyway",
				model, observedAt.UTC().Format("2006-01-02"))
		}
	}
	return &schema.VisionError{Model: model, Reason: reason, Detail: detail}
}

// beginTurnVision records the turn's vision facts on turnRequestOptions, from
// the same route and session override the pre-turn refusal above consulted.
// Both are per-turn state, so the retry gate and the request gate answer the
// same questions the refusal did, and a reset at turn end clears them with
// the rest of the options.
func (r *Runner) beginTurnVision(route routing.Route) {
	// An explicit vision = "on" makes the provider's own refusal the answer,
	// and so does this session's send-anyway answer for the same model: the
	// user was already told the model looks blind and chose to try anyway.
	// Reading only route.VisionForced here stripped that user's images on the
	// first rejection, silently converting the turn they insisted on into
	// text-only.
	r.turnRequestOptions.visionForced = route.VisionForced ||
		r.State.VisionForced(route.Preset.Provider, route.Preset.Model)
	// A route that resolved vision-off drops an image-bearing tool result's
	// images at the request gate instead of paying a rejection and a stripped
	// retry for each one. visionForced above exempts the whole turn.
	r.turnRequestOptions.visionOff = !route.Vision
}

// visionRejectedSince reports when the runtime last recorded this
// provider/model refusing image input, if that record is still live. It reads
// the same record the router applied when it took images off the route, with
// the same TTL and config-hash rules, so the refusal names the cause the
// route actually has rather than guessing at the preset.
func (r *Runner) visionRejectedSince(preset routing.ModelPreset) (time.Time, bool) {
	if r.VisionOverrides == nil {
		return time.Time{}, false
	}
	configHash := ""
	if r.ProviderConfigHash != nil {
		configHash = r.ProviderConfigHash(preset.Provider)
	}
	if !r.VisionOverrides.VisionRejected(preset.Provider, preset.Model, configHash, modelcache.DefaultCapabilityTTL, r.Now()) {
		return time.Time{}, false
	}
	rec, ok := r.VisionOverrides.Rejection(preset.Provider, preset.Model)
	return rec.ObservedAt, ok
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

// Notices the request gate appends when it removes images. One per message,
// however many refs went missing: repeating the line per ref makes a
// multi-image result read as several separate losses.
const (
	imageNotStoredNotice = "[image omitted: not in store]"
	imageVisionOffNotice = "[image omitted: model has vision off]"
)

// imageStatter is the optional Stat half of schema.ImageSource: a source that
// can say whether it still holds a ref without paying for its bytes.
// *imagepayload.Store implements it. A source that does not is left alone —
// the alternative, reading every image to find out, would make the gate a
// second copy of the request.
type imageStatter interface {
	Stat(ctx context.Context, hash string) (schema.ImageRef, bool, error)
}

// gateRequestImages prepares one request's wire for the route it is about to
// travel on. It is called from the single dispatch point (chatOnceAttempt),
// after the working-state tail is attached, so the inspection snapshot and
// every encoder see the same messages.
//
// Only this gate and the compaction thresholds drop images. The gate runs on
// the copy the caller is about to send, never on the runner's own slice, so
// the wire is recomputed per request and the notice cannot stack.
func (r *Runner) gateRequestImages(ctx context.Context, msgs []schema.ChatMessage) []schema.ChatMessage {
	out := msgs
	// A route that cannot take images gains nothing from a tool result's
	// images: the provider rejects the request and the retry that follows
	// strips every image anyway, so the cost of keeping them is a rejected
	// request and a lost round-trip per step. The prompt's own images are
	// refused before the turn starts, so only tool messages are handled
	// here, and an explicit send-anyway exempts the turn — the user asked
	// for the provider's own answer.
	if r.turnRequestOptions.visionOff && !r.turnRequestOptions.visionForced {
		out = dropImages(out, imageVisionOffNotice, func(m schema.ChatMessage) bool {
			return m.Role == schema.RoleTool
		})
	}
	// A session resumed after the store swept an entry still names the
	// evicted hash. The encoders fail the whole request on a ref they cannot
	// resolve, so one missing screenshot would make that session unable to
	// run again: drop the ref, keep the text, and say what went missing.
	return dropMissingImages(ctx, r.ImageSource, out)
}

// dropImages clears the images of every message pick accepts, once each, and
// appends notice to that message. The input is not modified: a slice is copied
// only when the first message actually changes.
func dropImages(msgs []schema.ChatMessage, notice string, pick func(schema.ChatMessage) bool) []schema.ChatMessage {
	var out []schema.ChatMessage
	for i := range msgs {
		if len(msgs[i].Images) == 0 || !pick(msgs[i]) {
			continue
		}
		if out == nil {
			out = append([]schema.ChatMessage(nil), msgs...)
		}
		out[i].Images = nil
		out[i].Content += "\n" + notice
	}
	if out == nil {
		return msgs
	}
	return out
}

// dropMissingImages removes refs src can no longer resolve, appending one
// notice per affected message. A source without Stat, or no source at all, is
// left alone: guessing would drop images that are present.
//
// A Stat error keeps the ref. The store failing to answer is not evidence the
// image is gone, and the encoder's own error is the more useful failure.
func dropMissingImages(ctx context.Context, src schema.ImageSource, msgs []schema.ChatMessage) []schema.ChatMessage {
	st, ok := src.(imageStatter)
	if !ok {
		return msgs
	}
	var out []schema.ChatMessage
	for i := range msgs {
		if len(msgs[i].Images) == 0 {
			continue
		}
		kept := make([]schema.ImageRef, 0, len(msgs[i].Images))
		for _, ref := range msgs[i].Images {
			if _, present, err := st.Stat(ctx, ref.Hash); err != nil || present {
				kept = append(kept, ref)
			}
		}
		if len(kept) == len(msgs[i].Images) {
			continue
		}
		if out == nil {
			out = append([]schema.ChatMessage(nil), msgs...)
		}
		out[i].Images = nil
		if len(kept) > 0 {
			out[i].Images = kept
		}
		out[i].Content += "\n" + imageNotStoredNotice
	}
	if out == nil {
		return msgs
	}
	return out
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
	configHash := ""
	if r.ProviderConfigHash != nil {
		configHash = r.ProviderConfigHash(providerName)
	}
	// Record takes the lock and returns the snapshot to persist, so a
	// concurrent route resolution can neither panick on a map write nor
	// catch a half-applied map on its way to disk.
	snapshot := r.VisionOverrides.Record(providerName, model, configHash, time.Now())
	_ = r.SaveVisionOverrides(snapshot)
}

// noteVisionRetry tells the user, in the transcript, that this turn went
// out without its images. Silence here would read as the model ignoring the
// screenshot.
func (r *Runner) noteVisionRetry() {
	r.State.AddMessage(session.RoleSystem,
		"the model rejected image input; resent without images",
		session.ContentTypeCompaction)
}
