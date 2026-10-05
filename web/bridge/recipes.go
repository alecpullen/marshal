package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Recipe kinds and structured outputs.
const (
	RecipePrompt = "prompt"

	OutputNone           = "none"
	OutputReviewFindings = "review-findings"
	OutputCIResult       = "ci-result"
)

var (
	// ErrInvalidRecipe marks a recipe the bridge refuses. HTTP maps it to 400.
	ErrInvalidRecipe = errors.New("bridge: invalid recipe")
	// ErrUnknownRecipe is returned when no recipe has the name.
	ErrUnknownRecipe = errors.New("bridge: unknown recipe")
	// ErrRecipeBuiltin is returned when a built-in recipe is changed or
	// deleted. Copy it under a new name to edit.
	ErrRecipeBuiltin = errors.New("bridge: built-in recipes cannot be changed; copy it under a new name")
	// ErrRecipeExists is returned when a copy would overwrite a recipe.
	ErrRecipeExists = errors.New("bridge: recipe already exists")
)

var (
	recipeNameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
	placeholderRe = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_-]+)\s*\}\}`)
)

// RecipeInput declares one placeholder a recipe's prompt may use.
type RecipeInput struct {
	Name     string `json:"name"`
	Label    string `json:"label,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// RecipeLimits bound one run. Zero means unlimited.
type RecipeLimits struct {
	MaxMinutes int     `json:"maxMinutes,omitempty"`
	MaxUSD     float64 `json:"maxUsd,omitempty"`
}

// Recipe is a stored, parameterised prompt (or run) with limits.
type Recipe struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	Kind        string          `json:"kind"`
	Mode        string          `json:"mode,omitempty"`
	Workspace   string          `json:"workspace,omitempty"`
	Routing     json.RawMessage `json:"routing,omitempty"`
	Inputs      []RecipeInput   `json:"inputs,omitempty"`
	Prompt      string          `json:"prompt"`
	Limits      *RecipeLimits   `json:"limits,omitempty"`
	Output      string          `json:"output,omitempty"`
	Builtin     bool            `json:"builtin,omitempty"`
}

// Validate checks a recipe's shape.
func (r Recipe) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalidRecipe}, a...)...)
	}
	if !recipeNameRe.MatchString(r.Name) {
		return bad("name must match %s", recipeNameRe)
	}
	switch r.Kind {
	case RecipePrompt, RunSDD, RunSwarm:
	default:
		return bad("kind must be prompt, sdd or swarm")
	}
	if r.Mode != "" && !validApprovalModes[r.Mode] {
		return bad("mode %q is not a valid approval mode", r.Mode)
	}
	switch r.Output {
	case "", OutputNone, OutputReviewFindings, OutputCIResult:
	default:
		return bad("output must be none, review-findings or ci-result")
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return bad("prompt is required")
	}
	declared := map[string]bool{}
	for _, in := range r.Inputs {
		if in.Name == "" || declared[in.Name] {
			return bad("input names must be non-empty and unique")
		}
		declared[in.Name] = true
	}
	for _, m := range placeholderRe.FindAllStringSubmatch(r.Prompt, -1) {
		if !declared[m[1]] {
			return bad("prompt uses {{%s}}, which is not a declared input", m[1])
		}
	}
	if l := r.Limits; l != nil && (l.MaxMinutes < 0 || l.MaxUSD < 0) {
		return bad("limits cannot be negative")
	}
	if len(r.Routing) > 0 && string(r.Routing) != "null" && !json.Valid(r.Routing) {
		return bad("routing is not valid JSON")
	}
	return nil
}

// RecipeStore keeps recipes as JSON files under <state>/recipes. Built-ins
// are merged in; a stored recipe of the same name overrides one.
type RecipeStore struct{ dir string }

// NewRecipeStore returns a store rooted at <stateDir>/recipes.
func NewRecipeStore(stateDir string) *RecipeStore {
	return &RecipeStore{dir: filepath.Join(stateDir, "recipes")}
}

func (s *RecipeStore) path(name string) string { return filepath.Join(s.dir, name+".json") }

func (s *RecipeStore) stored(name string) (Recipe, bool, error) {
	if !recipeNameRe.MatchString(name) {
		return Recipe{}, false, nil
	}
	data, err := os.ReadFile(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return Recipe{}, false, nil
	}
	if err != nil {
		return Recipe{}, false, err
	}
	var r Recipe
	if err := json.Unmarshal(data, &r); err != nil {
		return Recipe{}, false, fmt.Errorf("recipe %s is corrupt: %w", name, err)
	}
	r.Builtin = false
	return r, true, nil
}

// List returns every recipe, built-ins included, sorted by name.
func (s *RecipeStore) List() ([]Recipe, error) {
	byName := map[string]Recipe{}
	for _, b := range builtinRecipes() {
		byName[b.Name] = b
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		if r, found, err := s.stored(name); err == nil && found {
			byName[name] = r
		}
	}
	out := make([]Recipe, 0, len(byName))
	for _, r := range byName {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns a recipe by name.
func (s *RecipeStore) Get(name string) (Recipe, error) {
	if r, ok, err := s.stored(name); err != nil {
		return Recipe{}, err
	} else if ok {
		return r, nil
	}
	for _, b := range builtinRecipes() {
		if b.Name == name {
			return b, nil
		}
	}
	return Recipe{}, fmt.Errorf("%w: %s", ErrUnknownRecipe, name)
}

// Put validates and stores a recipe. A built-in is refused: the caller
// saves a copy under a new name instead.
func (s *RecipeStore) Put(r Recipe) error {
	if r.Builtin {
		return ErrRecipeBuiltin
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if isBuiltinRecipe(r.Name) {
		// A stored recipe may shadow a built-in, but only one that was
		// copied; refuse silently replacing one by accident.
		if _, ok, _ := s.stored(r.Name); !ok {
			return ErrRecipeBuiltin
		}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(s.path(r.Name), data, 0o600)
}

// Delete removes a stored recipe. A built-in cannot be deleted.
func (s *RecipeStore) Delete(name string) error {
	cur, err := s.Get(name)
	if err != nil {
		return err
	}
	if cur.Builtin {
		return ErrRecipeBuiltin
	}
	return os.Remove(s.path(name))
}

// Copy saves the recipe `from` under the new name `to`.
func (s *RecipeStore) Copy(from, to string) (Recipe, error) {
	src, err := s.Get(from)
	if err != nil {
		return Recipe{}, err
	}
	if !recipeNameRe.MatchString(to) {
		return Recipe{}, fmt.Errorf("%w: name must match %s", ErrInvalidRecipe, recipeNameRe)
	}
	if _, err := s.Get(to); err == nil {
		return Recipe{}, fmt.Errorf("%w: %s", ErrRecipeExists, to)
	}
	src.Name, src.Builtin = to, false
	if err := s.Put(src); err != nil {
		return Recipe{}, err
	}
	return src, nil
}
