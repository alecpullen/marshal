package bridge

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// hooksPrefix is where forges deliver webhooks. It is outside /api, so it
// takes no bearer token: the HMAC signature is the credential.
const hooksPrefix = "/hooks/forge/"

// maxHookBody bounds a webhook body.
const maxHookBody = 1 << 20

// hookSecretPath is where a repo's webhook secret lives in the provider.
func hookSecretPath(repoID string) string { return "hooks/" + repoID }

// prEvent is a pull request that opened, changed or became ready.
type prEvent struct {
	repoID  string
	number  int
	headSHA string
	// manual marks a run the operator asked for: the PR filters do not apply.
	manual bool
}

// checkEvent is a failed check. ref is the branch the commit is on and sha
// the commit itself.
type checkEvent struct {
	repoID string
	ref    string
	sha    string
}

// hookSignatureOK verifies the body against the secret for either forge's
// header. Both are HMAC-SHA256 over the raw body; GitHub prefixes "sha256=".
func hookSignatureOK(r *http.Request, secret, body []byte) bool {
	var got string
	switch {
	case r.Header.Get("X-Hub-Signature-256") != "":
		v := r.Header.Get("X-Hub-Signature-256")
		var ok bool
		if got, ok = strings.CutPrefix(v, "sha256="); !ok {
			return false
		}
	case r.Header.Get("X-Gitea-Signature") != "":
		got = r.Header.Get("X-Gitea-Signature")
	default:
		return false
	}
	want, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// hookEvent is a webhook reduced to what the automations act on.
type hookEvent struct {
	pr    *prEvent
	check *checkEvent
}

// parseHook maps a verified webhook to an event, or nil for the many it
// ignores.
func parseHook(r *http.Request, repoID string, body []byte) *hookEvent {
	event := r.Header.Get("X-GitHub-Event")
	if event == "" {
		event = r.Header.Get("X-Gitea-Event")
	}
	switch event {
	case "pull_request":
		var p struct {
			Action      string `json:"action"`
			Number      int    `json:"number"`
			PullRequest struct {
				Number int `json:"number"`
				Head   struct {
					SHA string `json:"sha"`
				} `json:"head"`
			} `json:"pull_request"`
		}
		if json.Unmarshal(body, &p) != nil {
			return nil
		}
		switch p.Action {
		case "opened", "synchronize", "synchronized", "reopened", "ready_for_review":
		default:
			return nil
		}
		n := p.Number
		if n == 0 {
			n = p.PullRequest.Number
		}
		if n == 0 {
			return nil
		}
		return &hookEvent{pr: &prEvent{repoID: repoID, number: n, headSHA: p.PullRequest.Head.SHA}}
	case "check_run", "check_suite", "workflow_run":
		var p struct {
			Action   string `json:"action"`
			CheckRun struct {
				Conclusion string `json:"conclusion"`
				HeadSHA    string `json:"head_sha"`
				Suite      struct {
					HeadBranch string `json:"head_branch"`
				} `json:"check_suite"`
			} `json:"check_run"`
			CheckSuite struct {
				Conclusion string `json:"conclusion"`
				HeadSHA    string `json:"head_sha"`
				HeadBranch string `json:"head_branch"`
			} `json:"check_suite"`
			WorkflowRun struct {
				Conclusion string `json:"conclusion"`
				HeadSHA    string `json:"head_sha"`
				HeadBranch string `json:"head_branch"`
			} `json:"workflow_run"`
		}
		if json.Unmarshal(body, &p) != nil || p.Action != "completed" {
			return nil
		}
		var conclusion, sha, branch string
		switch event {
		case "check_run":
			conclusion, sha, branch = p.CheckRun.Conclusion, p.CheckRun.HeadSHA, p.CheckRun.Suite.HeadBranch
		case "check_suite":
			conclusion, sha, branch = p.CheckSuite.Conclusion, p.CheckSuite.HeadSHA, p.CheckSuite.HeadBranch
		default:
			conclusion, sha, branch = p.WorkflowRun.Conclusion, p.WorkflowRun.HeadSHA, p.WorkflowRun.HeadBranch
		}
		if conclusion != "failure" || sha == "" {
			return nil
		}
		return &hookEvent{check: &checkEvent{repoID: repoID, ref: branch, sha: sha}}
	case "status":
		var p struct {
			State    string `json:"state"`
			SHA      string `json:"sha"`
			Branches []struct {
				Name string `json:"name"`
			} `json:"branches"`
		}
		if json.Unmarshal(body, &p) != nil || p.State != "failure" || p.SHA == "" {
			return nil
		}
		ev := &checkEvent{repoID: repoID, sha: p.SHA}
		if len(p.Branches) > 0 {
			ev.ref = p.Branches[0].Name
		}
		return &hookEvent{check: ev}
	}
	return nil
}

// hooksPublic serves POST /hooks/forge/{repoId}.
func (s *Server) hooksPublic(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil || r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	repoID := strings.TrimPrefix(r.URL.Path, hooksPrefix)
	if repoID == "" || strings.Contains(repoID, "/") {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	// An unknown repo, a missing secret and a bad signature all answer the
	// same, so the route does not say which repos exist.
	secret, err := s.fleet.secrets.Get(r.Context(), DefaultOwnerID, hookSecretPath(repoID))
	if err != nil || len(secret) == 0 || !hookSignatureOK(r, secret, body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	ev := parseHook(r, repoID, body)
	if ev == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.fleet.dispatchHook(*ev)
	w.WriteHeader(http.StatusAccepted)
}

// dispatchHook hands an event to its automation in its own goroutine, so
// the forge's delivery is answered at once.
func (f *Fleet) dispatchHook(ev hookEvent) {
	go func() {
		ctx, cancel := f.autoContext()
		defer cancel()
		switch {
		case ev.pr != nil:
			if f.auto.onPR != nil {
				f.auto.onPR(ctx, *ev.pr)
				return
			}
			f.onPREvent(ctx, *ev.pr)
		case ev.check != nil:
			if f.auto.onCheck != nil {
				f.auto.onCheck(ctx, *ev.check)
				return
			}
			f.onCheckEvent(ctx, *ev.check)
		}
	}()
}

// errHooksNeedBackend is returned when the secret backend cannot store the
// webhook secret.
var errHooksNeedBackend = errors.New("bridge: configure a secrets backend to issue webhook secrets")

// errHookSecretInUse is returned when a notification webhook already signs
// with the secret path a repo's webhook secret would take.
var errHookSecretInUse = errors.New("bridge: a notification webhook already uses this secret path; rename it first")

// IssueWebhookSecret generates a webhook secret for a repo, stores it, and
// returns it. The value is shown once: the provider holds the only copy.
func (f *Fleet) IssueWebhookSecret(ctx context.Context, repoID string) (string, error) {
	if _, ok := f.ws.Repo(repoID); !ok {
		return "", ErrUnknownRepo
	}
	// Notification webhooks keep their signing secrets under the same
	// hooks/ prefix, so refuse to overwrite one that shares this id.
	for _, h := range f.ws.Notifications().Webhooks {
		if h.SecretRef == secretRefPrefix+hookSecretPath(repoID) {
			return "", errHookSecretInUse
		}
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(b[:])
	if err := f.secrets.Put(ctx, DefaultOwnerID, hookSecretPath(repoID), []byte(secret)); err != nil {
		if errors.Is(err, ErrSecretsReadOnly) {
			return "", errHooksNeedBackend
		}
		return "", err
	}
	f.auditf(AuditEvent{Event: AuditWebhookSecretSet, OwnerID: DefaultOwnerID, RepoID: repoID})
	return secret, nil
}

func (s *Server) issueWebhookSecret(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	secret, err := s.fleet.IssueWebhookSecret(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, ErrUnknownRepo):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, errHooksNeedBackend), errors.Is(err, errHookSecretInUse):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"secret": secret})
	}
}
