// Package backend provides GBNF grammar generation for structured output.
package backend

// VerdictGrammar returns a GBNF grammar that constrains output to the
// critic verdict JSON schema. This eliminates "unparseable response"
// failures on local models.
//
// Schema:
//   {"verdict":"PASS|FAIL","summary":"...","issue":"...","fix":"...","concerns":["..."]}
func VerdictGrammar() string {
	return `root ::= object

object ::= "{" ws verdict "," ws summary "," ws issue "," ws fix "," ws concerns ws "}"

verdict ::= "\"verdict\"" ws ":" ws "\"" ("PASS" | "FAIL") "\""
summary ::= "\"summary\"" ws ":" ws string
issue ::= "\"issue\"" ws ":" ws string
fix ::= "\"fix\"" ws ":" ws string
concerns ::= "\"concerns\"" ws ":" ws array

array ::= "[" ws (string ("," ws string)*)? ws "]"

string ::= "\"" ([^"\\] | "\\" (["\\/bfnrt] | "u" [0-9a-fA-F]{4}))* "\""

ws ::= [ \t\n\r]*`
}

// JSONModeFor returns the appropriate JSONMode based on model capabilities.
// Local models (llama.cpp, Ollama with grammar support) should use JSONGrammar.
func JSONModeFor(subtype ProviderSubtype, supportsJSON bool) JSONMode {
	if !supportsJSON {
		return JSONNone
	}
	switch subtype {
	case SubtypeLlamaCPP:
		return JSONGrammar
	case SubtypeOllama:
		// Ollama supports format: "json" but not GBNF grammar directly
		// We'll use JSONLoose and rely on the retry loop for now
		return JSONLoose
	case SubtypeVLLM:
		// vLLM supports guided_json but not via standard OpenAI endpoint
		return JSONLoose
	default:
		return JSONLoose
	}
}
