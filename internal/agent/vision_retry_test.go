package agent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// visionFailProvider fails the first len(errs) calls synchronously, the way a
// real adapter surfaces an HTTP error (Chat returns nil, *ProviderError),
// then streams a plain answer.
type visionFailProvider struct {
	name  string
	errs  []error
	calls int
	reqs  []schema.ChatRequest
}

func (p *visionFailProvider) Name() string { return p.name }

func (p *visionFailProvider) Models(context.Context) ([]schema.ModelInfo, error) { return nil, nil }

func (p *visionFailProvider) Capabilities(context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func (p *visionFailProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	idx := p.calls
	p.calls++
	p.reqs = append(p.reqs, req)
	if idx < len(p.errs) && p.errs[idx] != nil {
		return nil, p.errs[idx]
	}
	ch := make(chan schema.ChatEvent, 2)
	ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: "hello"}
	ch <- schema.ChatEvent{Type: schema.ChatEventDone}
	close(ch)
	return ch, nil
}

func visionRejectionErr() error {
	return &provider.ProviderError{
		Provider:   "vision-fake",
		StatusCode: http.StatusBadRequest,
		Body:       `{"error":"this model does not support images"}`,
	}
}

func imageMessages() []schema.ChatMessage {
	return []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "what is in this screenshot?"},
		{Role: schema.RoleUser, Content: "and this one?", Images: []schema.ImageRef{{Hash: "abc", MIME: "image/png"}}},
	}
}

// joinContents concatenates every message body, so an assertion can look for a
// notice wherever the working-state tail sits relative to it.
func joinContents(msgs []schema.ChatMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// visionRunner builds a runner with the override wiring a real runtime has.
func visionRunner(t *testing.T, p provider.Provider) (*Runner, *modelcache.CapabilityOverrides, *modelcache.CapabilityOverrides) {
	t.Helper()
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	live := &modelcache.CapabilityOverrides{Rejections: map[string]modelcache.Rejection{}}
	saved := &modelcache.CapabilityOverrides{Rejections: map[string]modelcache.Rejection{}}
	r.VisionOverrides = live
	r.SaveVisionOverrides = func(o modelcache.CapabilityOverrides) error {
		*saved = o
		return nil
	}
	r.ProviderConfigHash = func(providerName string) string {
		return "hash:" + providerName
	}
	return r, live, saved
}

func TestIsVisionRejectionMatrix(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"400 image wording", http.StatusBadRequest, `{"error":"this model does not support images"}`, true},
		{"400 size wording", http.StatusBadRequest, `{"error":"context length exceeded"}`, false},
		{"415 image", http.StatusUnsupportedMediaType, `unsupported media type: image`, true},
		{"422 image", http.StatusUnprocessableEntity, `unprocessable: invalid image_url`, true},
		{"500 image", http.StatusInternalServerError, `image pipeline blew up`, false},
		{"not a provider error", 0, `image`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.status == 0 {
				err = errors.New("image")
			} else {
				err = &provider.ProviderError{StatusCode: tc.status, Body: tc.body}
			}
			if got := isVisionRejection(err); got != tc.want {
				t.Fatalf("isVisionRejection(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

func TestWithoutImagesAppendsNotice(t *testing.T) {
	msgs := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "first", Images: []schema.ImageRef{{Hash: "a"}}},
		{Role: schema.RoleUser, Content: "middle"},
		{Role: schema.RoleUser, Content: "last", Images: []schema.ImageRef{{Hash: "b"}}},
	}
	out, ok := withoutImages(msgs)
	if !ok {
		t.Fatal("withoutImages reported nothing to strip")
	}
	if carriesImages(out) {
		t.Fatal("images survived the strip")
	}
	if out[0].Content != "first" {
		t.Fatalf("the earlier image-bearing message was annotated: %q", out[0].Content)
	}
	want := "last\n[images omitted: model rejected image input]"
	if out[2].Content != want {
		t.Fatalf("last message = %q, want %q", out[2].Content, want)
	}
	if strings.Contains(out[1].Content, "images omitted") {
		t.Fatal("the notice landed on a message that carried no image")
	}
	// The input is untouched: the caller still holds the request it built.
	if !carriesImages(msgs) {
		t.Fatal("withoutImages mutated its input")
	}
	if _, ok := withoutImages([]schema.ChatMessage{{Role: schema.RoleUser, Content: "text only"}}); ok {
		t.Fatal("withoutImages reported a change for an image-free request")
	}
}

func TestRetryOnceOnVisionRejection(t *testing.T) {
	p := &visionFailProvider{name: "vision-fake", errs: []error{visionRejectionErr()}}
	r, live, saved := visionRunner(t, p)

	res, err := r.chatOnceAttempt(context.Background(), p, "test-model", imageMessages(), nil, false)
	if err != nil {
		t.Fatalf("expected the retry to succeed, got %v", err)
	}
	if res.Text == "" {
		t.Fatal("expected the retried answer's text")
	}
	if p.calls != 2 {
		t.Fatalf("expected exactly one retry (2 calls), got %d", p.calls)
	}
	if carriesImages(p.reqs[1].Messages) {
		t.Fatal("the retry still carried images")
	}
	if !strings.Contains(joinContents(p.reqs[1].Messages), "images omitted: model rejected image input") {
		t.Fatal("the retry did not announce the omission")
	}

	msgs := r.State.Messages()
	last := msgs[len(msgs)-1]
	if last.Role != session.RoleSystem || last.Content != "the model rejected image input; resent without images" {
		t.Fatalf("transcript notice = (%v, %q), want the vision-retry line", last.Role, last.Content)
	}

	key := "vision-fake/test-model"
	rec, ok := live.Rejections[key]
	if !ok {
		t.Fatalf("rejection for %q not recorded", key)
	}
	if rec.ConfigHash != "hash:vision-fake" {
		t.Fatalf("config hash = %q, want the ProviderConfigHash value", rec.ConfigHash)
	}
	if _, ok := saved.Rejections[key]; !ok {
		t.Fatal("the learned rejection was not persisted")
	}
}

func TestNoRetryForSizeRejection(t *testing.T) {
	p := &visionFailProvider{name: "vision-fake", errs: []error{&provider.ProviderError{
		StatusCode: http.StatusBadRequest,
		Body:       `{"error":"context length exceeded"}`,
	}}}
	r, live, saved := visionRunner(t, p)

	if _, err := r.chatOnceAttempt(context.Background(), p, "test-model", imageMessages(), nil, false); err == nil {
		t.Fatal("expected the size rejection to surface")
	}
	if p.calls != 1 {
		t.Fatalf("a size rejection must not retry, got %d calls", p.calls)
	}
	if len(live.Rejections) != 0 || len(saved.Rejections) != 0 {
		t.Fatalf("a size rejection must not be learned: live=%v saved=%v", live.Rejections, saved.Rejections)
	}
}

func TestNoRetryWhenForced(t *testing.T) {
	p := &visionFailProvider{name: "vision-fake", errs: []error{visionRejectionErr()}}
	r, live, saved := visionRunner(t, p)
	r.turnRequestOptions.visionForced = true

	_, err := r.chatOnceAttempt(context.Background(), p, "test-model", imageMessages(), nil, false)
	if err == nil {
		t.Fatal("expected an explicit vision=on to surface the provider's error")
	}
	if !isVisionRejection(err) {
		t.Fatalf("expected the original rejection, got %v", err)
	}
	if p.calls != 1 {
		t.Fatalf("a forced route must not retry, got %d calls", p.calls)
	}
	if len(live.Rejections) != 0 || len(saved.Rejections) != 0 {
		t.Fatalf("a forced route must not learn: live=%v saved=%v", live.Rejections, saved.Rejections)
	}
}

func TestNoPersistWhenRetryAlsoFails(t *testing.T) {
	second := errors.New("transport exploded")
	p := &visionFailProvider{name: "vision-fake", errs: []error{visionRejectionErr(), second}}
	r, live, saved := visionRunner(t, p)

	_, err := r.chatOnceAttempt(context.Background(), p, "test-model", imageMessages(), nil, false)
	if err == nil {
		t.Fatal("expected the second failure to surface")
	}
	if p.calls != 2 {
		t.Fatalf("expected one retry attempt (2 calls), got %d", p.calls)
	}
	if len(live.Rejections) != 0 || len(saved.Rejections) != 0 {
		t.Fatalf("a failed retry must not persist: live=%v saved=%v", live.Rejections, saved.Rejections)
	}
	for _, m := range r.State.Messages() {
		if m.Content == "the model rejected image input; resent without images" {
			t.Fatal("the retry notice was recorded for a retry that failed")
		}
	}
}
