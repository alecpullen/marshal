package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// idPattern bounds ids to something safe in URLs, audit lines and the
// UI: no spaces, slashes or control characters.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// credentialView is the wire shape of a credential. It never carries a
// value; Set says whether the secret it points at can be read now.
type credentialView struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	EnvVar  string `json:"envVar,omitempty"`
	KeyPath string `json:"keyPath,omitempty"`
	Ref     string `json:"ref,omitempty"`
	User    string `json:"user,omitempty"`
	Set     bool   `json:"set"`
}

// credentialSet reports whether the credential's secret exists. It only
// checks existence: the value is dropped immediately.
func (f *Fleet) credentialSet(ctx context.Context, c Credential) bool {
	switch c.Kind {
	case "none":
		return true
	case "pat":
		v, ok := os.LookupEnv(c.EnvVar)
		return ok && v != ""
	case "ssh":
		_, err := os.Stat(c.KeyPath)
		return err == nil
	case "vault":
		path, err := parseCredentialRef(c.Ref)
		if err != nil {
			return false
		}
		_, err = f.secrets.Get(ctx, c.OwnerID, path)
		return err == nil
	}
	return false
}

func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	out := []credentialView{}
	for _, c := range s.fleet.creds.List() {
		out = append(out, credentialView{
			ID: c.ID, Kind: c.Kind, EnvVar: c.EnvVar, KeyPath: c.KeyPath,
			Ref: c.Ref, User: c.User, Set: s.fleet.credentialSet(r.Context(), c),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// validateCredential checks the kind-specific fields.
func validateCredential(c Credential) error {
	if !idPattern.MatchString(c.ID) {
		return errors.New("id must be 1-64 letters, digits, '.', '_' or '-'")
	}
	switch c.Kind {
	case "none":
	case "pat":
		if c.EnvVar == "" {
			return errors.New("pat credentials need envVar")
		}
	case "ssh":
		if c.KeyPath == "" {
			return errors.New("ssh credentials need keyPath")
		}
		if strings.ContainsAny(c.KeyPath, " \t\n\"'\\;&|`$()") {
			return errors.New("keyPath must not contain spaces or shell metacharacters")
		}
	case "vault":
		if _, err := parseCredentialRef(c.Ref); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown kind %q: expected none, pat, ssh or vault", c.Kind)
	}
	return nil
}

func (s *Server) putCredential(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	var body struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		EnvVar  string `json:"envVar"`
		KeyPath string `json:"keyPath"`
		Ref     string `json:"ref"`
		User    string `json:"user"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	c := Credential{ID: body.ID, Kind: body.Kind, OwnerID: DefaultOwnerID, User: body.User}
	switch body.Kind {
	case "pat":
		c.EnvVar = body.EnvVar
	case "ssh":
		c.KeyPath = body.KeyPath
	case "vault":
		c.Ref = body.Ref
	}
	if err := validateCredential(c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.fleet.ws.PutCredential(c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.fleet.creds.Put(c)
	writeJSON(w, http.StatusOK, credentialView{
		ID: c.ID, Kind: c.Kind, EnvVar: c.EnvVar, KeyPath: c.KeyPath, Ref: c.Ref, User: c.User,
		Set: s.fleet.credentialSet(r.Context(), c),
	})
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	id := r.PathValue("id")
	for _, repo := range s.fleet.ws.Repos() {
		if repo.CredRef == id {
			writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("credential %q is used by repo %q", id, repo.ID)})
			return
		}
	}
	if err := s.fleet.ws.RemoveCredential(id); err != nil {
		if errors.Is(err, ErrUnknownCredential) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.fleet.creds.Remove(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.fleet.ws.Repos())
}

func (s *Server) registerRepo(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	var body struct {
		ID         string `json:"id"`
		URL        string `json:"url"`
		Branch     string `json:"branch"`
		Forge      string `json:"forge"`
		APIBase    string `json:"apiBase"`
		CredRef    string `json:"credRef"`
		Watch      bool   `json:"watch"`
		WatchLabel string `json:"watchLabel"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	bad := func(msg string) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
	}
	if !idPattern.MatchString(body.ID) {
		bad("id must be 1-64 letters, digits, '.', '_' or '-'")
		return
	}
	if strings.TrimSpace(body.URL) == "" {
		bad("url is required")
		return
	}
	repo := Repo{
		ID: body.ID, URL: strings.TrimSpace(body.URL), Branch: body.Branch,
		CredRef: body.CredRef, OwnerID: DefaultOwnerID, Forge: body.Forge,
		APIBase: body.APIBase, Watch: body.Watch, WatchLabel: body.WatchLabel,
	}
	if repo.Forge != "" {
		if _, err := ForgeFor(repo, forgeHTTPClient); err != nil {
			bad(fmt.Sprintf("unknown forge %q: expected github or gitea", repo.Forge))
			return
		}
	}
	if repo.Watch && repo.Forge == "" {
		bad("watch needs a forge")
		return
	}
	if repo.CredRef != "" {
		if c, ok := s.fleet.creds.Get(repo.CredRef); !ok || c.OwnerID != DefaultOwnerID {
			bad(fmt.Sprintf("unknown credential %q", repo.CredRef))
			return
		}
	}
	// Re-registering keeps the poller's bookkeeping.
	if old, ok := s.fleet.ws.Repo(repo.ID); ok {
		repo.LastPolled, repo.LastPollErr = old.LastPolled, old.LastPollErr
	}
	if err := s.fleet.ws.PutRepo(repo); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditRepoRegistered, OwnerID: DefaultOwnerID, RepoID: repo.ID})
	writeJSON(w, http.StatusOK, repo)
}

func (s *Server) removeRepo(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	id := r.PathValue("id")
	for _, a := range s.fleet.ws.Agents() {
		if a.SourceKind == "git" && a.SourceRef == id {
			writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("repo %q is used by agent %s", id, a.ID)})
			return
		}
	}
	if err := s.fleet.ws.RemoveRepo(id); err != nil {
		if errors.Is(err, ErrUnknownRepo) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditRepoRemoved, OwnerID: DefaultOwnerID, RepoID: id})
	w.WriteHeader(http.StatusNoContent)
}
