package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrUnknownReviewComment is returned for a comment id the agent doesn't have.
var ErrUnknownReviewComment = errors.New("bridge: unknown review comment")

var errInvalidReview = errors.New("bridge: invalid review comment")

// maxQuoteLines bounds the quoted diff lines sent to the agent.
const maxQuoteLines = 6

// ReviewComment is one line comment left on an agent's diff.
type ReviewComment struct {
	ID         string    `json:"id"`
	AgentID    string    `json:"agentId"`
	Path       string    `json:"path"`
	Line       int       `json:"line"`
	Side       string    `json:"side"`
	Quote      string    `json:"quote"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
	SentAt     time.Time `json:"sentAt"`
	ResolvedAt time.Time `json:"resolvedAt,omitzero"`
	OwnerID    string    `json:"ownerId"`
}

// reviewMessage is the text the agent receives for a comment.
func reviewMessage(c ReviewComment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Review comment on %s:%d (%s):\n", c.Path, c.Line, c.Side)
	if q := strings.TrimRight(c.Quote, "\n"); q != "" {
		lines := strings.Split(q, "\n")
		if len(lines) > maxQuoteLines {
			lines = lines[:maxQuoteLines]
		}
		for _, l := range lines {
			b.WriteString("> " + l + "\n")
		}
	}
	b.WriteString(c.Body)
	return b.String()
}

// AddReviewComment validates and stores a comment, then delivers it to the
// agent: as steering if a turn is running, else as a new prompt.
func (f *Fleet) AddReviewComment(ctx context.Context, agentID string, in ReviewComment) (ReviewComment, error) {
	if _, ok := f.ws.Agent(agentID); !ok {
		return ReviewComment{}, fmt.Errorf("%w: agent %s", ErrUnknownAgent, agentID)
	}
	switch {
	case strings.TrimSpace(in.Path) == "":
		return ReviewComment{}, fmt.Errorf("%w: path is required", errInvalidReview)
	case strings.TrimSpace(in.Body) == "":
		return ReviewComment{}, fmt.Errorf("%w: body is required", errInvalidReview)
	case in.Line <= 0:
		return ReviewComment{}, fmt.Errorf("%w: line must be positive", errInvalidReview)
	case in.Side != "old" && in.Side != "new":
		return ReviewComment{}, fmt.Errorf("%w: side must be old or new", errInvalidReview)
	}
	rt, err := f.RuntimeForSession(agentID)
	if err != nil {
		return ReviewComment{}, err
	}
	c := ReviewComment{
		ID: newAgentID(), AgentID: agentID, Path: in.Path, Line: in.Line, Side: in.Side,
		Quote: in.Quote, Body: in.Body, CreatedAt: time.Now().UTC(), OwnerID: DefaultOwnerID,
	}
	text := reviewMessage(c)
	sid := rt.sessionID
	if rt.reg.Sessions()[sid].Busy {
		if err := rt.reg.Steer(ctx, sid, text); err != nil {
			return ReviewComment{}, err
		}
	} else {
		go func() {
			if err := rt.reg.Prompt(context.Background(), sid, text); err != nil {
				slog.Default().Warn("webbridge: review comment prompt failed", "agent", agentID, "err", err)
			}
		}()
	}
	c.SentAt = time.Now().UTC()
	if err := f.ws.PutReviewComment(c); err != nil {
		return ReviewComment{}, err
	}
	f.auditf(AuditEvent{Event: AuditReviewComment, OwnerID: c.OwnerID, AgentID: agentID,
		Detail: c.Path + ":" + strconv.Itoa(c.Line)})
	return c, nil
}

// RecentPrompts lists distinct prompts, newest first. An empty project
// matches every agent.
func (f *Fleet) RecentPrompts(project string, limit int) []string {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	agents := f.ws.Agents()
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].CreatedAt.After(agents[j].CreatedAt) })
	out := []string{}
	seen := map[string]bool{}
	for _, a := range agents {
		if project != "" && a.Project != project {
			continue
		}
		p := strings.TrimSpace(a.Prompt)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) == limit {
			break
		}
	}
	return out
}

func (s *Server) listReviewComments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.fleet.ws.Agent(id); !ok {
		writeErr(w, fmt.Errorf("%w: agent %s", ErrUnknownAgent, id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": s.fleet.ws.ReviewComments(id)})
}

func (s *Server) addReviewComment(w http.ResponseWriter, r *http.Request) {
	var body ReviewComment
	if !decodeJSON(w, r, &body) {
		return
	}
	c, err := s.fleet.AddReviewComment(r.Context(), r.PathValue("id"), body)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) resolveReviewComment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.fleet.ws.Agent(id); !ok {
		writeErr(w, fmt.Errorf("%w: agent %s", ErrUnknownAgent, id))
		return
	}
	if err := s.fleet.ws.ResolveReviewComment(id, r.PathValue("cid"), time.Now().UTC()); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

func (s *Server) recentPrompts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	writeJSON(w, http.StatusOK, map[string]any{"prompts": s.fleet.RecentPrompts(r.URL.Query().Get("project"), limit)})
}
