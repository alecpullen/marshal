package bridge

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
)

func validRecipe() Recipe {
	return Recipe{Name: "hello", Kind: RecipePrompt, Mode: "plan", Prompt: "Say hi to {{who}}.",
		Inputs: []RecipeInput{{Name: "who", Required: true}}}
}

func TestRecipeValidation(t *testing.T) {
	if err := validRecipe().Validate(); err != nil {
		t.Fatalf("valid recipe refused: %v", err)
	}
	cases := map[string]func(*Recipe){
		"bad name":               func(r *Recipe) { r.Name = "Hello World" },
		"empty name":             func(r *Recipe) { r.Name = "" },
		"bad kind":               func(r *Recipe) { r.Kind = "script" },
		"bad mode":               func(r *Recipe) { r.Mode = "yolo" },
		"bad output":             func(r *Recipe) { r.Output = "xml" },
		"empty prompt":           func(r *Recipe) { r.Prompt = "  " },
		"undeclared placeholder": func(r *Recipe) { r.Prompt = "Hi {{who}} and {{other}}" },
		"duplicate input":        func(r *Recipe) { r.Inputs = append(r.Inputs, RecipeInput{Name: "who"}) },
		"negative limit":         func(r *Recipe) { r.Limits = &RecipeLimits{MaxUSD: -1} },
		"bad routing":            func(r *Recipe) { r.Routing = json.RawMessage(`{nope`) },
	}
	for name, mutate := range cases {
		r := validRecipe()
		mutate(&r)
		if err := r.Validate(); !errors.Is(err, ErrInvalidRecipe) {
			t.Errorf("%s: err = %v, want ErrInvalidRecipe", name, err)
		}
	}
}

func TestRecipeBuiltinsAreListedAndValid(t *testing.T) {
	s := NewRecipeStore(t.TempDir())
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Recipe{}
	for _, r := range list {
		got[r.Name] = r
		if err := r.Validate(); err != nil {
			t.Errorf("built-in %s invalid: %v", r.Name, err)
		}
		if !r.Builtin {
			t.Errorf("%s not marked builtin", r.Name)
		}
	}
	for _, name := range []string{"review-pr", "fix-ci", "summarize-changes", "update-deps"} {
		if _, ok := got[name]; !ok {
			t.Errorf("built-in %s missing", name)
		}
	}
	if r := got["review-pr"]; r.Mode != "plan" || r.Output != OutputReviewFindings {
		t.Errorf("review-pr = mode %q output %q", r.Mode, r.Output)
	}
	if r := got["fix-ci"]; r.Mode != "edit" || r.Output != OutputCIResult ||
		!strings.Contains(r.Prompt, "Reproduce first") || !strings.Contains(r.Prompt, "reproduced: false") {
		t.Errorf("fix-ci = %+v", r)
	}
}

func TestRecipeStoredCopyOverridesBuiltin(t *testing.T) {
	dir := t.TempDir()
	s := NewRecipeStore(dir)
	over := Recipe{Name: "review-pr", Title: "Mine", Kind: RecipePrompt, Prompt: "Look at it."}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(over)
	if err := os.WriteFile(s.path("review-pr"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("review-pr")
	if err != nil || got.Title != "Mine" || got.Builtin {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	list, _ := s.List()
	n := 0
	for _, r := range list {
		if r.Name == "review-pr" {
			n++
			if r.Title != "Mine" {
				t.Errorf("list shows the built-in, not the override: %+v", r)
			}
		}
	}
	if n != 1 {
		t.Errorf("review-pr listed %d times", n)
	}
	// Deleting the override brings the built-in back.
	if err := s.Delete("review-pr"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get("review-pr"); !got.Builtin {
		t.Errorf("built-in did not come back: %+v", got)
	}
}

func TestRecipeBuiltinsCannotBeChangedOrDeleted(t *testing.T) {
	s := NewRecipeStore(t.TempDir())
	b, _ := s.Get("fix-ci")
	if err := s.Put(b); !errors.Is(err, ErrRecipeBuiltin) {
		t.Errorf("Put(builtin) = %v", err)
	}
	b.Builtin = false
	if err := s.Put(b); !errors.Is(err, ErrRecipeBuiltin) {
		t.Errorf("Put under a built-in's name = %v", err)
	}
	if err := s.Delete("fix-ci"); !errors.Is(err, ErrRecipeBuiltin) {
		t.Errorf("Delete(builtin) = %v", err)
	}
	if err := s.Delete("nope"); !errors.Is(err, ErrUnknownRecipe) {
		t.Errorf("Delete(unknown) = %v", err)
	}
}

func TestRecipePutGetDeleteAndCopy(t *testing.T) {
	s := NewRecipeStore(t.TempDir())
	if err := s.Put(validRecipe()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get("hello"); err != nil || got.Prompt != validRecipe().Prompt {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	cp, err := s.Copy("review-pr", "review-mine")
	if err != nil || cp.Builtin || cp.Name != "review-mine" {
		t.Fatalf("Copy = %+v, %v", cp, err)
	}
	if got, _ := s.Get("review-mine"); got.Output != OutputReviewFindings {
		t.Errorf("copy lost its output: %+v", got)
	}
	if _, err := s.Copy("review-pr", "review-mine"); !errors.Is(err, ErrRecipeExists) {
		t.Errorf("copy over an existing recipe = %v", err)
	}
	if _, err := s.Copy("review-pr", "fix-ci"); !errors.Is(err, ErrRecipeExists) {
		t.Errorf("copy over a built-in = %v", err)
	}
	if _, err := s.Copy("review-pr", "Bad Name"); !errors.Is(err, ErrInvalidRecipe) {
		t.Errorf("copy to a bad name = %v", err)
	}
	if _, err := s.Copy("nope", "x"); !errors.Is(err, ErrUnknownRecipe) {
		t.Errorf("copy of an unknown recipe = %v", err)
	}
	if err := s.Delete("review-mine"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("review-mine"); !errors.Is(err, ErrUnknownRecipe) {
		t.Errorf("deleted recipe still found: %v", err)
	}
}

func TestRecipeRoutes(t *testing.T) {
	f := testFleet(t)
	f.audit = NewAuditLog(t.TempDir())
	s := NewServer(f, "")

	rec := doReq(t, s, http.MethodGet, "/api/recipes", nil, nil)
	var list []Recipe
	decodeBody(t, rec, &list)
	if rec.Code != http.StatusOK || len(list) != 4 {
		t.Fatalf("list = %d %d recipes", rec.Code, len(list))
	}
	body := validRecipe()
	if rec := doReq(t, s, http.MethodPut, "/api/recipes/hello", body, nil); rec.Code != http.StatusOK {
		t.Fatalf("put = %d %s", rec.Code, rec.Body.String())
	}
	if findEvent(auditTail(t, f), AuditRecipeSaved) == nil {
		t.Error("save not audited")
	}
	if rec := doReq(t, s, http.MethodPut, "/api/recipes/other", body, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("put with a mismatched name = %d", rec.Code)
	}
	bad := body
	bad.Prompt = "{{ghost}}"
	if rec := doReq(t, s, http.MethodPut, "/api/recipes/hello", bad, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("put of an invalid recipe = %d", rec.Code)
	}
	if rec := doReq(t, s, http.MethodPut, "/api/recipes/review-pr", Recipe{Kind: RecipePrompt, Prompt: "x"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("put over a built-in = %d", rec.Code)
	}
	if rec := doReq(t, s, http.MethodGet, "/api/recipes/nope", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown = %d", rec.Code)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/recipes/review-pr/copy", map[string]string{"name": "mine"}, nil); rec.Code != http.StatusCreated {
		t.Errorf("copy = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, s, http.MethodPost, "/api/recipes/review-pr/copy", map[string]string{"name": "mine"}, nil); rec.Code != http.StatusConflict {
		t.Errorf("second copy = %d", rec.Code)
	}
	if rec := doReq(t, s, http.MethodDelete, "/api/recipes/mine", nil, nil); rec.Code != http.StatusOK {
		t.Errorf("delete = %d", rec.Code)
	}
	if findEvent(auditTail(t, f), AuditRecipeDeleted) == nil {
		t.Error("delete not audited")
	}
	if rec := doReq(t, s, http.MethodDelete, "/api/recipes/fix-ci", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("delete built-in = %d", rec.Code)
	}
}
