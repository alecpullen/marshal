// Package usability drives the real Marshal binary through scripted scenarios
// and checks the visible screen.
//
// # What the suite proves
//
// A scripted scenario sends input and waits for an explicit postcondition on
// the current viewport: the actor cannot report success until the screen itself
// shows the expected outcome. Scenarios run against a test-owned home, config,
// data directory and provider fixture, so the default suite needs no developer
// configuration, credentials or running service.
//
// # The exploratory LLM scenario
//
// TestExploratoryLLMSubtractFix is opt-in (USABILITY_LLM_MODEL) and is not a
// regression gate. Its verdict is the actor's own claim that it edited the
// fixture, and nothing in this package runs the fixture's `go test` as an
// independent oracle. A success there means the model said it finished, not
// that the code is correct; it must not be cited as evidence of correctness and
// is deliberately excluded from the scripted CI selection.
package usability
