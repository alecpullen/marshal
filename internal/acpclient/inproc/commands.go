package inproc

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/commands"
)

// maxActionResults bounds how many results' row actions a session keeps. A
// person drills through one panel at a time, so the newest sixteen results are
// plenty, and the map never grows with the session's life.
const maxActionResults = 16

var (
	errNoCommands  = errors.New("inproc: the session has no command registry")
	errStaleAction = errors.New("inproc: that action is no longer available; run its command again")
)

// Commands lists the session's slash commands in name order, hidden ones
// included. A closed session still answers: it is a read.
func (s *Session) Commands() []acpwire.CommandInfo {
	if s.cmds == nil {
		return nil
	}
	cmds := s.cmds.ListAll()
	if len(cmds) == 0 {
		return nil
	}
	out := make([]acpwire.CommandInfo, len(cmds))
	for i, c := range cmds {
		out[i] = commands.Info(c)
	}
	return out
}

// Command runs the named command's handler over the session with args. It makes
// no busy check (decision 7): a command's handler is the one that decides what
// a running turn means for it.
func (s *Session) Command(ctx context.Context, name string, args []string) (acpwire.CommandResult, error) {
	if s.closed.Load() {
		return acpwire.CommandResult{}, errSessionClosed
	}
	if s.cmds == nil {
		return acpwire.CommandResult{}, errNoCommands
	}
	cmd, ok := s.cmds.Lookup(name)
	if !ok {
		return acpwire.CommandResult{}, fmt.Errorf("inproc: no command /%s", name)
	}
	if cmd.Handler == nil {
		return acpwire.CommandResult{}, nil
	}
	return s.wireResult(cmd.Handler(s.state, args)), nil
}

// CommandAction runs the row action id names and returns its output. The action
// runs outside the lock, so it may mint actions of its own.
func (s *Session) CommandAction(ctx context.Context, id string) (acpwire.CommandResult, error) {
	if s.closed.Load() {
		return acpwire.CommandResult{}, errSessionClosed
	}
	s.mu.Lock()
	action, ok := s.actions[id]
	s.mu.Unlock()
	if !ok {
		return acpwire.CommandResult{}, errStaleAction
	}
	return s.wireResult(action(s.state)), nil
}

// wireResult converts res, minting an ID for each of its row actions and
// keeping them for CommandAction.
func (s *Session) wireResult(res commands.Result) acpwire.CommandResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	out := commands.WireResultOf(res, func(action func(*session.State) commands.Result) string {
		s.actionSeq++
		id := "a" + strconv.FormatUint(s.actionSeq, 10)
		s.actions[id] = action
		ids = append(ids, id)
		return id
	})
	if len(ids) == 0 {
		return out
	}
	s.actionResults = append(s.actionResults, ids)
	for len(s.actionResults) > maxActionResults {
		oldest := s.actionResults[0]
		for _, id := range oldest {
			delete(s.actions, id)
		}
		s.actionResults = s.actionResults[1:]
	}
	return out
}
