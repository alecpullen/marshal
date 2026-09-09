package session

import (
	"sort"
)

// SkillGateAction is what the runner should do with a skill.load call
// after consulting the session's gate state.
type SkillGateAction int

const (
	// SkillGateProceed: dispatch the load normally (gate off, threshold
	// exceeded, or the skill is session-approved).
	SkillGateProceed SkillGateAction = iota
	// SkillGatePrompt: window is small or unknown and the skill has no
	// sticky decision (or its deny counter hit the re-prompt multiple):
	// ask the user.
	SkillGatePrompt
	// SkillGateDenied: a sticky deny is active and this attempt is not
	// the every-3rd re-prompt: refuse immediately without prompting.
	SkillGateDenied
)

// SkillGateRePromptEvery is the deny-counter modulus: a sticky-denied
// skill re-prompts on every Nth re-attempt so a genuine "the situation
// changed" can succeed within the session. 3 means the cycle is
// deny-deny-prompt, bounding prompt spam to one per three attempts.
const SkillGateRePromptEvery = 3

// SkillGateDecision is one skill's session-scope gate state, for the
// /skills panel.
type SkillGateDecision struct {
	Skill   string
	Allowed bool
	Count   int // deny counter; 0 for allowed entries
}

// SkillGateApplies consults the gate for one skill.load call.
//
// threshold is the configured [skills] load_gate_threshold_tokens; window
// is the turn's context window (0 = unknown). The gate fires when the
// window is unknown or <= threshold, the gate is not disabled for the
// session, and the skill has no sticky session allow. A sticky deny
// returns SkillGateDenied unless the deny counter has reached a multiple
// of SkillGateRePromptEvery, in which case the bounded re-prompt fires.
// threshold <= 0 disables the gate before anything else is consulted.
func (s *State) SkillGateApplies(name string, threshold, window int) SkillGateAction {
	s.mu.Lock()
	defer s.mu.Unlock()
	if threshold <= 0 || s.skillGateDisabled {
		return SkillGateProceed
	}
	if window > 0 && window > threshold {
		return SkillGateProceed
	}
	if s.skillGateAllowed[name] {
		return SkillGateProceed
	}
	if n := s.skillGateDenied[name]; n > 0 && n%SkillGateRePromptEvery != 0 {
		return SkillGateDenied
	}
	return SkillGatePrompt
}

// SkillGateRecordAllow records the user's session-scope approval. allSkills
// also disables the gate for the rest of the session ("allow always (all
// skills)"). A per-skill allow clears that skill's deny counter so the
// counter does not resurface after an allow.
func (s *State) SkillGateRecordAllow(name string, allSkills bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skillGateAllowed == nil {
		s.skillGateAllowed = make(map[string]bool)
	}
	s.skillGateAllowed[name] = true
	delete(s.skillGateDenied, name)
	if allSkills {
		s.skillGateDisabled = true
	}
}

// SkillGateRecordDeny increments the skill's sticky deny counter.
func (s *State) SkillGateRecordDeny(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skillGateDenied == nil {
		s.skillGateDenied = make(map[string]int)
	}
	s.skillGateDenied[name]++
}

// SkillGateSetEnabled flips the session-scope gate override used by the
// /skills panel. Re-enabling clears every sticky decision so a re-tighten
// starts fresh; disabling only flips the flag (existing decisions become
// moot until the gate is re-enabled, which clears them).
func (s *State) SkillGateSetEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if enabled {
		s.skillGateDisabled = false
		s.skillGateAllowed = make(map[string]bool)
		s.skillGateDenied = make(map[string]int)
		return
	}
	s.skillGateDisabled = true
}

// SkillGateEnabled reports the session-scope override flag. Note the gate
// can still be inert when the configured threshold is 0; the /skills panel
// renders both facts.
func (s *State) SkillGateEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.skillGateDisabled
}

// SkillGateDecisions returns the sticky decisions sorted by skill name.
func (s *State) SkillGateDecisions() []SkillGateDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SkillGateDecision, 0, len(s.skillGateAllowed)+len(s.skillGateDenied))
	for name := range s.skillGateAllowed {
		out = append(out, SkillGateDecision{Skill: name, Allowed: true})
	}
	for name, count := range s.skillGateDenied {
		if s.skillGateAllowed[name] {
			continue
		}
		out = append(out, SkillGateDecision{Skill: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Skill < out[j].Skill })
	return out
}

// SkillGateClearDecision drops one skill's sticky decision so the next
// load re-evaluates from scratch (the /skills panel's per-decision flip).
func (s *State) SkillGateClearDecision(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.skillGateAllowed, name)
	delete(s.skillGateDenied, name)
}
