package agent

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"marshal/internal/llm/embedding"
	"marshal/internal/skills"
)

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
// all silent no-ops.
func (r *Runner) computeSkillHints(ctx context.Context, goal string) {
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
	e := r.SkillEmbedder
	if e == nil {
		e = resolveEmbedder(r.State.Config)
	}
	if e == nil {
		return
	}
	if r.skillRanker == nil {
		r.skillRanker = newSkillRanker()
	}
	r.skillHints = r.skillRanker.rank(ctx, e, candidates, goal)
}
