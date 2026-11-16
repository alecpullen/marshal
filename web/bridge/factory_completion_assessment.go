package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// factoryAssessmentInputDigest is the identity of everything a completion
// assessment is about: the goal, the criteria the latest approval bound it to,
// and the linked pull requests. It is what makes a result checkable, so a
// result computed against other inputs reads as stale instead of describing
// work it never saw.
//
// The encoding is stable and explicit. Fields are written as
// "<tag><len>:<value>\n", so no value can be confused with a field boundary,
// and every link is closed by a "--\n" separator, so an absent link list is
// distinct from any number of links. The link list is sorted by (repo ID, node
// ID) — the immutable identity of a link — so the digest depends on the set of
// linked pull requests and never on the order they were added. Each link
// contributes its repository, number, node ID, head SHA, base ref and state.
// Summary, ObservedAt and Fresh are not inputs: they describe an observation
// of these inputs rather than the inputs themselves.
func factoryAssessmentInputDigest(i FactoryItem) string {
	var b strings.Builder
	write := func(tag, value string) {
		b.WriteString(tag)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(len(value)))
		b.WriteByte(':')
		b.WriteString(value)
		b.WriteByte('\n')
	}
	write("goal", i.Title)
	criteria := ""
	if n := len(i.Approvals); n > 0 {
		criteria = i.Approvals[n-1].Spec
	}
	write("criteria", criteria)
	var links []FactoryPRLink
	if i.Completion != nil {
		links = i.Completion.Links
	}
	links = append([]FactoryPRLink(nil), links...)
	sort.Slice(links, func(a, c int) bool {
		if links[a].RepoID != links[c].RepoID {
			return links[a].RepoID < links[c].RepoID
		}
		return links[a].PR.NodeID < links[c].PR.NodeID
	})
	for _, l := range links {
		write("repo", l.RepoID)
		write("number", strconv.Itoa(l.PR.Number))
		write("node", l.PR.NodeID)
		write("head", l.PR.HeadSHA)
		write("base", l.PR.BaseRef)
		write("state", l.PR.State)
		b.WriteString("--\n")
	}
	return factoryDigest([]byte(b.String()))
}

// withAssessmentFreshness derives the freshness of every assessment an item
// carries: the item's own and the copy in each completion record. Freshness is
// a reading, never a journal write, so it is computed here and nowhere else,
// and an assessment is fresh exactly when the digest it was computed against
// is the item's current one. A nil assessment stays nil.
func withAssessmentFreshness(i FactoryItem) FactoryItem {
	if i.Completion == nil {
		return i
	}
	digest := factoryAssessmentInputDigest(i)
	if a := i.Completion.Assessment; a != nil {
		a.Fresh = a.InputDigest == digest
	}
	for n := range i.Completion.Records {
		if a := i.Completion.Records[n].Assessment; a != nil {
			a.Fresh = a.InputDigest == digest
		}
	}
	return i
}

// assessCompletion answers an explicitly requested completion assessment. It
// is an assessment of the business outcome, never a gate on it: nothing here
// completes, reopens or stops anything, and no completion path consults it.
//
// The request journals its intent before any adapter runs, so a lost answer
// leaves a pending result rather than nothing, and the intent's request
// identity is the command's own — a retry replays the journalled item and
// never asks the adapter twice. The adapter runs outside every store update:
// an assessment is external work, and the journal is a boundary. With no
// adapter there is nothing to ask, so the recorded state is "unavailable": the
// absence of an assessment is a state the surfaces render, and it starts no
// planner session of its own.
//
// The result is stored with the digest the request was made against, even when
// the inputs moved while the adapter ran: a result that describes older inputs
// must not claim to describe the current ones. Freshness, computed on every
// read, is what tells a surface that it has moved on.
func (a *factoryAPI) assessCompletion(ctx context.Context, id string, cmd FactoryCommand) (FactoryItem, error) {
	now := time.Now().UTC()
	raw, err := json.Marshal(cmd)
	if err != nil {
		return FactoryItem{}, err
	}
	if a.priorRequest(cmd.RequestID) {
		item, err := a.store.Update(id, cmd.RequestID, cmd.ExpectedVersion, raw, nil, now)
		if err != nil {
			return FactoryItem{}, err
		}
		return withAssessmentFreshness(item), nil
	}
	item, err := a.store.Get(id)
	if err != nil {
		return FactoryItem{}, err
	}
	digest := factoryAssessmentInputDigest(item)
	pending := FactoryAssessment{Status: "pending", InputDigest: digest}
	requested, err := a.store.Update(id, cmd.RequestID, cmd.ExpectedVersion, raw, func(dst *FactoryItem) error {
		completion := factoryCompletionOf(*dst)
		completion.Assessment = factoryCloneAssessment(&pending)
		dst.Completion = &completion
		dst.History = append(dst.History, FactoryEvent{At: now, Kind: "completion_assessment_requested", Message: "completion assessment requested"})
		return nil
	}, now)
	if err != nil {
		return FactoryItem{}, err
	}
	result := FactoryAssessment{Status: "unavailable", InputDigest: digest}
	if a.assessor != nil {
		assessed, assessErr := a.assessor.AssessFactoryCompletion(ctx, requested, factoryCopy(completionLinksOf(requested)))
		switch {
		case assessErr != nil:
			// A failure is recorded evidence about the assessment, not about
			// the work: it is stored beside the summary a later request
			// replaces, and it never reaches the caller as an error.
			result.Status = "failed"
			result.Summary = assessErr.Error()
		default:
			result.Status = "available"
			result.Summary = assessed.Summary
			result.Source = assessed.Source
		}
	}
	result.ObservedAt = now
	// The item is read again after the adapter ran: the result is applied at
	// whatever version the item reached meanwhile, so work done while the
	// assessment was in flight is not refused by it. The digest stored with
	// the result is the pending one — the inputs the assessment actually saw —
	// and a recomputed digest that differs is exactly the staleness the read
	// path reports.
	current, err := a.store.Get(id)
	if err != nil {
		return FactoryItem{}, err
	}
	message := fmt.Sprintf("completion assessment %s", result.Status)
	if factoryAssessmentInputDigest(current) != digest {
		message += "; the inputs changed while it ran"
	}
	outcome, err := json.Marshal(struct {
		Kind   string `json:"kind"`
		Status string `json:"status"`
		Digest string `json:"digest"`
	}{Kind: "assess_completion_result", Status: result.Status, Digest: result.InputDigest})
	if err != nil {
		return FactoryItem{}, err
	}
	saved, err := a.store.Update(id, cmd.RequestID+"-result", current.Version, outcome, func(dst *FactoryItem) error {
		completion := factoryCompletionOf(*dst)
		completion.Assessment = factoryCloneAssessment(&result)
		dst.Completion = &completion
		dst.History = append(dst.History, FactoryEvent{At: now, Kind: "completion_assessment_recorded", Message: message})
		return nil
	}, now)
	if err != nil {
		return FactoryItem{}, err
	}
	return withAssessmentFreshness(saved), nil
}

// completionLinksOf is the link list an assessment is asked about.
func completionLinksOf(i FactoryItem) []FactoryPRLink {
	if i.Completion == nil {
		return nil
	}
	return i.Completion.Links
}
