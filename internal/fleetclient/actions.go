package fleetclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"marshal/internal/acpclient"
	"marshal/web/bridge/contract"
)

// post sends one action: body encoded as JSON when it is not nil, out
// decoded when it is not nil. A non-2xx answer is a *acpclient.FleetError.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	return c.action(ctx, http.MethodPost, path, body, out)
}

// getAction reads one action's answer with post's refusal handling: an action's
// read refuses the same way its writes do (decision 2).
func (c *Client) getAction(ctx context.Context, path string, out any) error {
	return c.action(ctx, http.MethodGet, path, nil, out)
}

// action sends one action's request. Every action goes through here, so every
// action's refusal is the seam's error whatever its method.
func (c *Client) action(ctx context.Context, method, path string, body, out any) error {
	var sent io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("fleet action %s: %w", path, err)
		}
		sent = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.transport.BaseURL+path, sent)
	if err != nil {
		return fmt.Errorf("fleet action: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := c.authorize(ctx, req); err != nil {
		return err
	}
	resp, err := c.actionClient().Do(req)
	if err != nil {
		// The bridge was not reached: status 0 and the dial's text (decision 2).
		return &acpclient.FleetError{Message: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return actionError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("fleet action %s: %w", path, err)
	}
	return nil
}

// actionClient is the transport's HTTP client with redirects refused: a
// redirect answers as a refusal instead of re-sending the request elsewhere.
func (c *Client) actionClient() *http.Client {
	client := *c.httpClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}

// actionError turns a refused answer into the seam's error (decision 2).
func actionError(resp *http.Response) error {
	// A late answer to a settled request is not an error: something else
	// answered it first.
	if resp.StatusCode == http.StatusGone {
		return acpclient.ErrSettled
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var refusal struct {
		Error string `json:"error"`
		Scope string `json:"scope"`
	}
	_ = json.Unmarshal(body, &refusal)

	message := strings.TrimSpace(refusal.Error)
	if resp.StatusCode == http.StatusTooManyRequests && refusal.Scope != "" {
		// The budget refusal's sentence is the web UI's, not the wire's.
		message = fmt.Sprintf("Budget reached (%s)", refusal.Scope)
	}
	return &acpclient.FleetError{Status: resp.StatusCode, Message: message}
}

// Spawn starts an agent.
func (c *Client) Spawn(ctx context.Context, req contract.SpawnRequest) (contract.SpawnResult, error) {
	var out contract.SpawnResult
	err := c.post(ctx, "/api/agents", req, &out)
	return out, err
}

// AnswerPermission answers an agent's approval.
func (c *Client) AnswerPermission(ctx context.Context, id string, d contract.Decision) error {
	return c.post(ctx, "/api/permissions/"+url.PathEscape(id), d, nil)
}

// AnswerQuestion answers an agent's question.
func (c *Client) AnswerQuestion(ctx context.Context, id string, a contract.Answers) error {
	return c.post(ctx, "/api/questions/"+url.PathEscape(id), a, nil)
}

// Cancel stops an agent's turn.
func (c *Client) Cancel(ctx context.Context, agentID string) error {
	return c.post(ctx, "/api/sessions/"+url.PathEscape(agentID)+"/cancel", nil, nil)
}

// ApproveIntake spawns a submission's agent.
func (c *Client) ApproveIntake(ctx context.Context, id string) (contract.ApproveResult, error) {
	var out contract.ApproveResult
	err := c.post(ctx, "/api/pending/"+url.PathEscape(id)+"/approve", nil, &out)
	return out, err
}

// DenyIntake discards a submission without spawning.
func (c *Client) DenyIntake(ctx context.Context, id string) error {
	return c.post(ctx, "/api/pending/"+url.PathEscape(id)+"/deny", nil, nil)
}

// RecentPrompts is a project's last prompts, newest first.
func (c *Client) RecentPrompts(ctx context.Context, project string, limit int) ([]string, error) {
	query := url.Values{}
	query.Set("project", project)
	query.Set("limit", strconv.Itoa(limit))

	var out contract.RecentPrompts
	if err := c.getAction(ctx, "/api/prompts/recent?"+query.Encode(), &out); err != nil {
		return nil, err
	}
	return out.Prompts, nil
}
