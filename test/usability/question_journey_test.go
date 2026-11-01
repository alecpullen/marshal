package usability

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/test/usability/harness"
	"marshal/test/usability/screen"
)

// questionTexts are the three free-text questions the scripted question.ask
// call asks, in the order it asks them. They carry no options, so each renders
// as a plain text input and Enter submits it.
var questionTexts = []string{"Name?", "Colour?", "Shape?"}

// questionPanelTitle is the header the framed console draws over the live
// question form. It is the panel's own title, not a transcript row, so its
// disappearance is what proves the form closed.
const questionPanelTitle = "? Question"

// questionResultHeader opens the tool-role message the agent loop builds for a
// question.ask call that got at least one answer
// (internal/agent/execute.go, executeNativeQuestionAsk). Each answer follows
// as a `- "question": "answer"` line, in the order the questions were asked.
const questionResultHeader = "User answers:"

// questionDeclinedResult is the whole tool-role message the agent loop sends
// when every answer came back session.AnswerUnanswered. That branch is taken
// only when no question carries a real answer, so this exact message is the
// agent's evidence that all three were skipped — no pair is sent, and a
// fabricated value has nowhere to hide in it.
const questionDeclinedResult = "The user declined to answer every question. Proceed with your best judgment."

// questionFocusPrompt is the prompt huh draws for the focused text input. While
// the question form is open it replaces the composer, so the last such line in
// the viewport belongs to the focused question.
const questionFocusPrompt = "❯"

// TestScriptedQuestionsPartialSkip drives a real three-question form at 80x24
// twice: once answering the first free-text question and then skipping, and
// once skipping from the first question.
//
// What the agent is told is asserted from the provider's record of the
// tool-role message question.ask sent back — never from the model's final text,
// which is only the completion signal that the turn settled. A skipped answer
// must reach the model as session.AnswerUnanswered, never as a value the test
// (or the UI) invented, and a completed answer must survive the skip.
//
// Each case gets its own session, its own fixture provider, and the isolated
// child config's bounded context window, so neither case can inherit the
// other's script, screen or turn.
func TestScriptedQuestionsPartialSkip(t *testing.T) {
	bin := requireBinary(t)

	for _, tc := range []struct {
		name string
		// answerFirst types and submits an answer to Name? before skipping.
		answerFirst bool
		// want is the ordered answer line per question in the tool-role message
		// the agent builds. A nil want means every question was skipped, which
		// the agent collapses into questionDeclinedResult instead.
		want []string
	}{
		{
			name:        "partial",
			answerFirst: true,
			want: wantQuestionAnswerLines(
				"alice",
				session.AnswerUnanswered,
				session.AnswerUnanswered,
			),
		},
		{
			name: "skip_all",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			questions := make([]map[string]string, len(questionTexts))
			for i, q := range questionTexts {
				questions[i] = map[string]string{"question": q}
			}
			p := newFixtureProvider(t, []scriptedReply{
				{ToolName: "question.ask", ToolArgs: mustJSON(t, map[string]any{"questions": questions})},
				{Text: "finished"},
			})
			env := isolatedAgentEnv(t, p.URL())

			sess, err := harness.New(harness.Config{
				BinaryPath: bin,
				Width:      80,
				Height:     24,
				WorkDir:    t.TempDir(),
				Env:        env.Env,
			})
			if err != nil {
				t.Fatalf("harness.New: %v", err)
			}
			defer sess.Close()

			waitForViewport(t, sess, 60*time.Second, "idle composer", func(scr screen.Screen) bool {
				return strings.Contains(scr.Content, promptMarker)
			})

			if err := sess.Send("Ask me the three questions."); err != nil {
				t.Fatalf("Send(goal): %v", err)
			}
			if err := sess.SendKey("enter"); err != nil {
				t.Fatalf("SendKey(enter): %v", err)
			}

			// The live form must be showing, with the first question focused:
			// only the focused question's own input is drawn, so a form that
			// opened on the wrong question would fail here rather than silently
			// answering the wrong row.
			waitForViewport(t, sess, 60*time.Second, "question form at Name?", func(scr screen.Screen) bool {
				return strings.Contains(scr.Content, questionPanelTitle) &&
					questionFocused(scr, 0)
			})

			if tc.answerFirst {
				if err := sess.Send("alice"); err != nil {
					t.Fatalf("Send(answer): %v", err)
				}
				// The typed text is on the focused input before Enter is pressed:
				// what is submitted is what the user saw, not a race with the
				// form's own repaint.
				waitForViewport(t, sess, 30*time.Second, "typed answer", func(scr screen.Screen) bool {
					return questionFocused(scr, 0) && strings.Contains(scr.Content, "alice")
				})
				if err := sess.SendKey("enter"); err != nil {
					t.Fatalf("SendKey(enter) submit Name?: %v", err)
				}
				// Waiting for the focus to be on Colour? is what proves the first
				// answer was actually submitted: the submitted input is no longer
				// rendered, and the next question's input is.
				waitForViewport(t, sess, 30*time.Second, "question form at Colour?", func(scr screen.Screen) bool {
					return questionFocused(scr, 1) && !strings.Contains(scr.Content, "alice")
				})
			} else {
				// Skipping from the first question: the form is still asking
				// Name?, so nothing has been answered yet.
				if !questionFocused(snapshotScreen(t, sess), 0) {
					t.Fatalf("skip_all did not reach the form with Name? focused")
				}
			}

			// Escape is the skip: the focused question and every question after
			// it become Unanswered.
			if err := sess.SendKey("esc"); err != nil {
				t.Fatalf("SendKey(esc): %v", err)
			}

			waitForViewport(t, sess, 60*time.Second, "question form closed", func(scr screen.Screen) bool {
				return !strings.Contains(scr.Content, questionPanelTitle)
			})

			// The turn settles only once the scripted final reply is on screen:
			// the answers reached the model and it answered.
			waitForViewport(t, sess, 60*time.Second, "turn settled", func(scr screen.Screen) bool {
				return strings.Contains(scr.Content, "finished")
			})

			// The provider's record is the independent oracle for what the tool
			// reported to the agent.
			msgs := waitForToolMessages(t, p, 1, 60*time.Second, "question tool result")
			if len(msgs) != 1 {
				t.Fatalf("provider recorded %d tool messages, want 1: %+v", len(msgs), msgs)
			}
			if msgs[0].CallID != "call_1" {
				t.Fatalf("tool result call id = %q, want call_1", msgs[0].CallID)
			}

			if tc.want == nil {
				// Every question was skipped. The agent loop reports that as one
				// decline message — the branch it takes only when every answer is
				// session.AnswerUnanswered — and sends no pair at all, so no
				// fabricated value can stand in for a skipped answer.
				if msgs[0].Content != questionDeclinedResult {
					t.Fatalf("skip-all tool result = %q, want %q", msgs[0].Content, questionDeclinedResult)
				}
				if got := answerLines(msgs[0].Content); len(got) != 0 {
					t.Fatalf("skip-all tool result carries answer lines %q, want none", got)
				}
			} else {
				if !strings.HasPrefix(msgs[0].Content, questionResultHeader+"\n") {
					t.Fatalf("tool result = %q, want it to report %q", msgs[0].Content, questionResultHeader)
				}
				// The recorded lines are exactly the expected ones, in the order
				// the questions were asked. Comparing the whole ordered list is
				// what rules out a fabricated value standing in for a skipped
				// answer: a skipped question may carry the sentinel and nothing
				// else.
				if got := answerLines(msgs[0].Content); !equalStrings(got, tc.want) {
					t.Fatalf("recorded answers = %q, want %q", got, tc.want)
				}
			}

			// An extra completion, a stray endpoint or an unreadable body means
			// the turn did something the script does not describe.
			p.AssertNoUnexpectedRequests(t)
		})
	}
}

// wantQuestionAnswerLines renders the one answer line the agent reports per
// question, in the `- "question": "answer"` shape executeNativeQuestionAsk
// builds. A skipped question carries session.AnswerUnanswered, so a fabricated
// value would not match.
func wantQuestionAnswerLines(answers ...string) []string {
	lines := make([]string, len(answers))
	for i, answer := range answers {
		lines[i] = fmt.Sprintf("- %q: %q", questionTexts[i], answer)
	}
	return lines
}

// answerLines extracts the ordered answer lines from a recorded tool-role
// message, ignoring its header. Anything that is not one of these lines — a
// stray sentence, an invented pair — is not silently accepted: the caller
// compares the whole list.
func answerLines(content string) []string {
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, `- "`) && strings.Contains(line, `": `) {
			lines = append(lines, line)
		}
	}
	return lines
}

// equalStrings reports whether got and want hold the same strings in the same
// order.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// questionFocused reports whether question i is the one the form is asking.
// The panel renders the focused field's own view directly under that
// question's text, so its prompt line sits between question i and question
// i+1. That is what distinguishes a form that advanced past an answered
// question from one that merely has every question's text on screen — all
// three questions are drawn at once, only the focused one owns an input.
func questionFocused(scr screen.Screen, i int) bool {
	if i < 0 || i >= len(questionTexts) {
		return false
	}
	at := lineIndex(scr.Lines, questionTexts[i])
	if at < 0 {
		return false
	}
	focus := focusedPromptIndex(scr.Lines)
	if focus <= at {
		return false
	}
	if i+1 < len(questionTexts) {
		next := lineIndexFrom(scr.Lines, questionTexts[i+1], at+1)
		if next < 0 || focus >= next {
			return false
		}
	}
	return true
}

// focusedPromptIndex is the line of the focused input's prompt. The question
// form replaces the composer while it is open, so the last prompt line in the
// viewport belongs to the focused field.
func focusedPromptIndex(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], questionFocusPrompt) {
			return i
		}
	}
	return -1
}

// lineIndex is the first viewport line containing text, or -1.
func lineIndex(lines []string, text string) int { return lineIndexFrom(lines, text, 0) }

// lineIndexFrom is the first viewport line at or after start containing text,
// or -1.
func lineIndexFrom(lines []string, text string, start int) int {
	if start < 0 {
		start = 0
	}
	for i := start; i < len(lines); i++ {
		if strings.Contains(lines[i], text) {
			return i
		}
	}
	return -1
}

// snapshotScreen parses the session's current viewport, failing the test rather
// than reading a broken screen.
func snapshotScreen(t *testing.T, s *harness.Session) screen.Screen {
	t.Helper()
	scr, err := screen.Parse(s.Snapshot())
	if err != nil {
		t.Fatalf("parse current viewport: %v", err)
	}
	return scr
}
