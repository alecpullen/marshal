package agent

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/llm/embedding"
	"marshal/internal/skills"
)

const (
	// skillAutoLoadMaxK bounds how many skills a turn may auto-load.
	skillAutoLoadMaxK = 2
	// skillAutoLoadMinScore is the cosine floor for auto-loading a skill.
	// Descriptions are short, so only a clearly-on-topic match clears it.
	skillAutoLoadMinScore = 0.55
	// skillAutoLoadTimeout caps the embed round-trip so a slow embedding
	// endpoint cannot stall the turn.
	skillAutoLoadTimeout = 10 * time.Second
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

// rank returns the names of up to skillAutoLoadMaxK skills whose description
// similarity to goal is at least skillAutoLoadMinScore, best first. Any
// embed failure returns nil — ranking is best-effort and must never break a
// turn.
func (s *skillRanker) rank(ctx context.Context, e embedding.Embedder, candidates []skills.Skill, goal string) []string {
	if e == nil || len(candidates) == 0 || strings.TrimSpace(goal) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, skillAutoLoadTimeout)
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
		if score := cosineSim(goalVec, vecs[i]); score >= skillAutoLoadMinScore {
			hits = append(hits, scored{sk.Name, score})
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > skillAutoLoadMaxK {
		hits = hits[:skillAutoLoadMaxK]
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

// maybeAutoLoadSkills embeds the turn goal and quietly loads the
// top-matching skills so their bodies are present in the first prompt
// (appendSkillBodies picks up anything activated before the prompt build).
// General role only. Embeddings unconfigured, embed errors, and timeouts
// are all silent no-ops.
func (r *Runner) maybeAutoLoadSkills(ctx context.Context, goal string) {
	if r.role() != RoleGeneral || r.SkillIndex == nil || r.State == nil {
		return
	}
	all := r.SkillIndex.List()
	candidates := make([]skills.Skill, 0, len(all))
	for _, sk := range all {
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
	var loaded []string
	for _, name := range r.skillRanker.rank(ctx, e, candidates, goal) {
		// Quiet: no per-skill transcript tag, exempt from max_active.
		// Per-skill errors (budget, missing) are ignored — auto-load is
		// best-effort.
		if err := skills.LoadSkillIntoSessionQuiet(r.SkillIndex, r.State, name); err == nil {
			loaded = append(loaded, name)
		}
	}
	// One aggregate record per turn, replacing the per-skill tags the quiet
	// path deliberately suppresses. A turn that loads nothing writes nothing.
	if len(loaded) > 0 {
		r.State.AddMessage(session.RoleSystem, strings.Join(loaded, "\n"), session.ContentTypeSkillAuto)
	}
}
