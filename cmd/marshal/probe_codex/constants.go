//go:build probe_c

package main

import "fmt"

// Every value in this file is transcribed from the codex CLI source tree
// (openai/codex @ 44fe510ce3ee61c8ef623adcbf89b901c73ddd61) as captured in
// .docs-archive/superpowers/plans/2026-09-14-codex-source-notes.md.
// Each constant carries a `// source:` comment naming the file it came from.
// A value that Task 1 could not establish is left as "" and the consuming
// subcommand must refuse to run.

const (
	// source: codex-rs/login/src/auth/manager.rs:1717 (pub const CLIENT_ID)
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// source: codex-rs/login/src/server.rs:76 (DEFAULT_ISSUER)
	codexIssuer = "https://auth.openai.com"

	// source: codex-rs/login/src/server.rs:602 (format!("{issuer}/oauth/authorize"))
	codexAuthorizeURL = codexIssuer + "/oauth/authorize"

	// source: codex-rs/login/src/server.rs:711 (format!("{}/oauth/token", ...))
	codexTokenURL = codexIssuer + "/oauth/token"

	// source: codex-rs/login/src/server.rs:604-606
	codexScope = "openid profile email offline_access api.connectors.read api.connectors.invoke"

	// source: codex-rs/login/src/server.rs:77 (DEFAULT_PORT)
	codexRedirectPort = 1455

	// source: codex-rs/login/src/server.rs:79 (FALLBACK_PORT)
	codexRedirectFallbackPort = 1457

	// source: codex-rs/login/src/server.rs:167 (format!("http://127.0.0.1:{actual_port}/auth/callback"))
	codexRedirectPath = "/auth/callback"

	// source: codex-rs/login/src/auth/default_client.rs:42 (DEFAULT_ORIGINATOR)
	codexOriginator = "codex_cli_rs"

	// source: codex-rs/model-provider-info/src/lib.rs:77 (CHATGPT_CODEX_BASE_URL)
	codexChatBaseURL = "https://chatgpt.com/backend-api/codex"

	// source: codex-rs/codex-api/src/endpoint/responses.rs:143 (Method::POST, "/responses")
	codexResponsesPath = "/responses"

	// source: codex-rs/codex-api/src/endpoint/models.rs:33-35 (fn path() -> "models")
	codexModelsPath = "/models"

	// source: codex-rs/model-provider/src/bearer_auth_provider.rs:36
	codexAccountIDHeader = "ChatGPT-Account-ID"

	// source: codex-rs/model-provider/src/bearer_auth_provider.rs:39-41
	codexFedrampHeader = "X-OpenAI-Fedramp"

	// source: codex-rs/login/src/device_code_auth.rs:169 (format!("{api_base_url}/deviceauth/usercode"))
	codexDeviceUserCodeURL = codexIssuer + "/api/accounts/deviceauth/usercode"

	// source: codex-rs/login/src/device_code_auth.rs:189 (format!("{api_base_url}/deviceauth/token"))
	codexDeviceTokenURL = codexIssuer + "/api/accounts/deviceauth/token"

	// source: codex-rs/login/src/device_code_auth.rs:175 (format!("{base_url}/codex/device"))
	codexDeviceVerificationURL = codexIssuer + "/codex/device"

	// source: codex-rs/login/src/device_code_auth.rs:206 (format!("{base_url}/deviceauth/callback"))
	codexDeviceRedirectURI = codexIssuer + "/deviceauth/callback"

	// source: codex-rs/models-manager/src/manager.rs:31 (MODEL_CACHE_FILE),
	// joined onto $CODEX_HOME at :282; $CODEX_HOME defaults to ~/.codex
	// (codex-rs/utils/home-dir/src/lib.rs:52-60).
	codexModelsCacheFile = "models_cache.json"

	// source: codex-rs/login/src/success_page.rs:106-130 (jwt_auth_claims) and
	// codex-rs/login/src/server.rs:849-853 (chatgpt_account_id lookup).
	codexAuthClaimPath  = "https://api.openai.com/auth"
	codexAccountIDClaim = "chatgpt_account_id"

	// source: codex-rs/login/src/server.rs:594-601 (extra authorize params)
	codexParamIDTokenAddOrganizations = "id_token_add_organizations"
	codexParamSimplifiedFlow          = "codex_cli_simplified_flow"
	codexParamOriginator              = "originator"
)

// codexRequired reports whether a Task 1 constant was actually established.
// The probe refuses to run a subcommand whose constants are missing rather
// than guessing a value.
func codexRequired(name, value string) error {
	if value == "" {
		return fmt.Errorf("value for %s not established in Task 1", name)
	}
	return nil
}
