package contract

import "encoding/json"

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

// Recipe is a stored, parameterised prompt (or run) with limits:
// GET /api/recipes' answer and the body PUT /api/recipes/{name} takes
// (web/bridge/recipes.go).
//
// A built-in recipe cannot be changed or deleted; the bridge refuses both, and
// copying it under a new name is the way to edit one.
type Recipe struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind"`
	Mode        string `json:"mode,omitempty"`
	// Auto is the starting Auto choice; absent means the recipe sets none.
	Auto      *bool           `json:"auto,omitempty"`
	Workspace string          `json:"workspace,omitempty"`
	Routing   json.RawMessage `json:"routing,omitempty"`
	Inputs    []RecipeInput   `json:"inputs,omitempty"`
	Prompt    string          `json:"prompt"`
	Limits    *RecipeLimits   `json:"limits,omitempty"`
	Output    string          `json:"output,omitempty"`
	// Builtin marks a recipe the bridge ships, which is copy only.
	Builtin bool `json:"builtin,omitempty"`
	// Diagnostic reports why a stored recipe could not be read or validated.
	// It never appears on a built-in or a successfully saved recipe.
	Diagnostic string `json:"diagnostic,omitempty"`
}

// RecipeRun is the body POST /api/recipes/{name}/run takes: where the run
// starts, and the value of each of the recipe's inputs.
type RecipeRun struct {
	Project string            `json:"project"`
	RepoID  string            `json:"repoId"`
	Ref     string            `json:"ref"`
	Inputs  map[string]string `json:"inputs"`
}

// RecipeCopy is the body POST /api/recipes/{name}/copy takes: the name the
// copy is stored under.
type RecipeCopy struct {
	Name string `json:"name"`
}
