package agent

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"marshal/internal/llm/embedding"
	"marshal/internal/skills"
)

// investigationPatterns marks a question-class goal as an open-ended
// investigation, which is what the systematic-debugging skill covers —
// including the fix-less "why does X behave inconsistently" shape that a
// fix-oriented description alone does not surface. Checked case-insensitively.
var investigationPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bwhy\b`),
	regexp.MustCompile(`\bhow come\b`),
	regexp.MustCompile(`\binvestigat(?:e|ing|ion)\b`),
	regexp.MustCompile(`\bfigure out\b`),
	regexp.MustCompile(`\broot cause\b`),
	regexp.MustCompile(`\bdoesn'?t work\b`),
	regexp.MustCompile(`\bnot working\b`),
	regexp.MustCompile(`\bbroken\b`),
	regexp.MustCompile(`\binconsisten(?:t|cy|cies)\b`),
	regexp.MustCompile(`\bflaky\b`),
	regexp.MustCompile(`\bregress(?:ed|ion)\b`),
}

// classDefaultHints returns deterministic skill suggestions for a turn
// class, independent of any embedding model. These are suggestions only —
// the model decides whether to load — but they guarantee the core suite is
// surfaced on the turns where it matters, even with hints otherwise off.
func classDefaultHints(class TaskClass, goal string) []string {
	switch class {
	case ClassEdit:
		return []string{"test-driven-development", "verification-before-completion"}
	case ClassQuestion:
		lower := strings.ToLower(goal)
		for _, p := range investigationPatterns {
			if p.MatchString(lower) {
				return []string{"systematic-debugging"}
			}
		}
	}
	return nil
}

const (
	// skillHintMaxK bounds how many skills a turn may hint at. Three fits
	// the measured signal: top-1 accuracy is 6/7 when a skill genuinely
	// applies, and the runner-up is usually the second-best real answer.
	skillHintMaxK = 3
	// skillHintMinScore is a noise floor, not a decision boundary. Measured
	// separation between "a skill applies" and "no skill applies" is
	// NEGATIVE (worst true positive 0.486 < best true negative 0.586), so
	// no threshold can gate loading. This floor only drops obvious garbage
	// from a suggestion list the model is free to ignore.
	skillHintMinScore = 0.40
	// skillHintTimeout caps the embed round-trip so a slow embedding
	// endpoint cannot stall the turn.
	skillHintTimeout = 10 * time.Second
)

// skillRanker ranks skill descriptions against a turn goal using the
// configured embedding preset. Vectors are cached for the ranker's life,
// keyed by embedder model + skill name — skills are loaded once at startup,
// so a per-session cache never goes stale.
type skillRanker struct {
	cache map[string][]float32
}

func newSkillRanker() *skillRanker {
	return &skillRanker{cache: make(map[string][]float32)}
}

// rank returns the names of up to skillHintMaxK skills whose description
// similarity to goal is at least skillHintMinScore, best first. Any
// embed failure returns nil — ranking is best-effort and must never break a
// turn.
func (s *skillRanker) rank(ctx context.Context, e embedding.Embedder, candidates []skills.Skill, goal string) []string {
	if e == nil || len(candidates) == 0 || strings.TrimSpace(goal) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, skillHintTimeout)
	defer cancel()

	texts := make([]string, 0, len(candidates)+1)
	texts = append(texts, goal)
	uncached := make([]int, 0, len(candidates)) // candidate index per uncached text
	vecs := make([][]float32, len(candidates))
	for i, sk := range candidates {
		if v, ok := s.cache[e.Model()+"\x00"+sk.Name]; ok {
			vecs[i] = v
			continue
		}
		uncached = append(uncached, i)
		texts = append(texts, sk.Name+": "+sk.Description)
	}
	out, err := e.Embed(ctx, texts)
	if err != nil || len(out) != len(texts) {
		return nil
	}
	goalVec := out[0]
	for j, ci := range uncached {
		vecs[ci] = out[1+j]
		s.cache[e.Model()+"\x00"+candidates[ci].Name] = out[1+j]
	}

	type scored struct {
		name  string
		score float64
	}
	var hits []scored
	for i, sk := range candidates {
		if score := cosineSim(goalVec, vecs[i]); score >= skillHintMinScore {
			hits = append(hits, scored{sk.Name, score})
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > skillHintMaxK {
		hits = hits[:skillHintMaxK]
	}
	names := make([]string, len(hits))
	for i, h := range hits {
		names[i] = h.name
	}
	return names
}

// cosineSim mirrors retrieval.cosine (unexported there); duplicated here to
// avoid exporting retrieval internals for one caller.
func cosineSim(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// computeSkillHints ranks skills against the turn goal and records the top
// matches on the runner for the prompt builder to surface. It deliberately
// does NOT load anything.
//
// Measured on this project's configured embedder over its installed skills,
// cosine similarity ranks well but gates terribly: the worst true positive
// (0.486) scores below the best true negative (0.586), so any auto-load
// threshold either fires on unrelated work or never fires at all. Prefixing
// (nomic search_query/search_document) and mean-centering were both tested
// and made it worse. The model has the whole conversation and can judge
// applicability; the ranker only narrows the roster.
//
// General role only. Unconfigured embeddings, embed errors, and timeouts are
// all silent no-ops for the ranked path; class defaults still apply.
func (r *Runner) computeSkillHints(ctx context.Context, goal string, class TaskClass) {
	r.skillHints = nil
	if r.role() != RoleGeneral || r.SkillIndex == nil || r.State == nil {
		return
	}
	all := r.SkillIndex.List()
	candidates := make([]skills.Skill, 0, len(all))
	for _, sk := range all {
		// An active skill's body is already on the wire; hinting it is noise.
		if r.State.HasActiveSkill(sk.Name) {
			continue
		}
		candidates = append(candidates, sk)
	}
	if len(candidates) == 0 {
		return
	}

	// Class defaults come first so deterministic suggestions survive even
	// when ranking is a no-op (no embedder configured). Ranked hints, when
	// present, are merged ahead of them: a measured match is more specific
	// than a class-level one. Both are capped at skillHintMaxK.
	defaults := classDefaultHints(class, goal)
	e := r.SkillEmbedder
	if e == nil {
		e = resolveEmbedder(r.State.Config)
	}
	var ranked []string
	if e != nil {
		if r.skillRanker == nil {
			r.skillRanker = newSkillRanker()
		}
		ranked = r.skillRanker.rank(ctx, e, candidates, goal)
	}

	available := make(map[string]bool, len(candidates))
	for _, sk := range candidates {
		available[sk.Name] = true
	}
	merged := make([]string, 0, len(ranked)+len(defaults))
	seen := make(map[string]bool, len(ranked)+len(defaults))
	for _, name := range append(append([]string{}, ranked...), defaults...) {
		// Ranked names are drawn from candidates, but a class default may
		// name a skill that is not installed — hinting it would only
		// produce a failed skill.load.
		if seen[name] || !available[name] || r.State.HasActiveSkill(name) {
			continue
		}
		seen[name] = true
		merged = append(merged, name)
		if len(merged) >= skillHintMaxK {
			break
		}
	}
	r.skillHints = merged
}
