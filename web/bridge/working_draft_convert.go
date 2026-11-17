package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

// Converting a working draft is a handoff across two independent journals: the
// draft store durably reserves the intent, the factory journal records the
// created item, and the draft store then durably associates it. Each step is
// idempotent on its own and reconciliation finishes whatever a crash left
// between them, so one exact reviewed version of a draft becomes at most one
// factory draft.
//
// Nothing the browser sends is trusted as an identity: the factory request ID
// and the association's journal identity are both derived from the draft and
// the conversion request the browser named, and the factory request itself is
// the server's own reading of the reviewed content.

// errWorkingDraftTampered is a conversion whose caller-supplied factory request
// does not describe the reviewed draft. It is a conflict: the browser must
// re-read the draft and review it again.
var errWorkingDraftTampered = fmt.Errorf("%w: factory request does not match the reviewed values", ErrWorkingDraftConflict)

// frozenFactoryCreator is the factory seam a conversion uses: the read-only
// preflight that freezes a request, and the create that stores only a request
// it is given without resolving anything again. *factoryAPI is the production
// implementation; tests inject a fake so each persistence boundary can fail on
// purpose.
type frozenFactoryCreator interface {
	PrepareFrozen(FactoryCreate) (FactoryCreate, string, error)
	CreateFrozen(FactoryCreate, FactoryCreate) (FactoryItem, error)
}

// settingsVersionSource is the conditional-write token of a project's settings,
// which is the exact version a conversion is reviewed against. *Workspace
// implements it.
type settingsVersionSource interface {
	ProjectSettingsETag(root string) (string, error)
}

// workingDraftConverter converts working drafts into factory drafts. It is
// instance-scoped: one converter serves one draft store, so startup
// reconciliation and new conversions of the same draft share one per-draft
// lock.
type workingDraftConverter struct {
	store    *WorkingDraftStore
	factory  frozenFactoryCreator
	settings settingsVersionSource

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newWorkingDraftConverter(store *WorkingDraftStore, factory frozenFactoryCreator, settings settingsVersionSource) *workingDraftConverter {
	return &workingDraftConverter{store: store, factory: factory, settings: settings, locks: map[string]*sync.Mutex{}}
}

// lockDraft takes the lock that serializes every conversion and reconciliation
// of one draft and returns its release. Conversion of different drafts stays
// concurrent; a per-draft lock is kept for the life of the converter, which
// bounds them by the number of drafts it has touched.
func (c *workingDraftConverter) lockDraft(id string) func() {
	c.mu.Lock()
	m, ok := c.locks[id]
	if !ok {
		m = &sync.Mutex{}
		c.locks[id] = m
	}
	c.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// workingDraftConversionID derives one stable, namespaced identity from a draft
// and the conversion request that names it. Deriving instead of accepting an ID
// is what stops a browser from naming a factory request ID another handoff
// already used: the prefix keeps these out of any namespace a client chooses,
// and the digest binds the ID to exactly one draft and one conversion.
func workingDraftConversionID(prefix, draftID, conversionRequestID string) string {
	sum := sha256.Sum256([]byte(draftID + "\x00" + conversionRequestID))
	return prefix + hex.EncodeToString(sum[:])[:32]
}

// derivedFactoryRequestID is the factory request ID a conversion uses. It is
// recorded in both journals, so a crash replays exactly one factory request.
func derivedFactoryRequestID(draftID, conversionRequestID string) string {
	return workingDraftConversionID("wd-", draftID, conversionRequestID)
}

// derivedDoneID is the journal identity of the association a conversion makes.
func derivedDoneID(draftID, conversionRequestID string) string {
	return workingDraftConversionID("wd-done-", draftID, conversionRequestID)
}

// derivedFailID is the journal identity of a conversion the factory refused.
// It is a different operation kind from the association, so the two never
// replay each other.
func derivedFailID(draftID, conversionRequestID string) string {
	return workingDraftConversionID("wd-failed-", draftID, conversionRequestID)
}

// Convert turns one exact reviewed version of a draft into at most one factory
// draft, and reports the state it reached.
//
// The order is the whole design. Preflight resolves and validates the
// configuration and reads the project's settings version without writing
// anything; the caller's claimed settings version and factory request are
// checked against the server's own; the intent is then reserved durably against
// the exact draft version and settings version, which is the linearization
// point; and only after that is the factory asked to create anything. A crash
// on either side of a step leaves a state reconciliation can finish, and a
// retry with the same conversion request replays instead of repeating.
//
// Lock order, bounded and never inverted:
//
//	converter per-draft mutex -> draft store mutex (inside one store call)
//	                          -> factory API, and its journal, last
//
// The per-draft mutex is held across the factory preflight and the create, so
// competing conversions of one draft are serialized by construction while other
// drafts proceed. The draft store's mutex is only ever taken inside a single
// store method, is never held across a factory call, and no store mutex is held
// while the per-draft mutex is taken. The factory store's mutex is reached only
// from inside the factory API, after the draft store has released its own.
func (c *workingDraftConverter) Convert(ctx context.Context, id, requestID string, expectedVersion uint64, request FactoryCreate, settingsVersion string) (WorkingDraft, *FactoryItem, error) {
	if strings.TrimSpace(requestID) == "" {
		return WorkingDraft{}, nil, fmt.Errorf("requestId required")
	}
	unlock := c.lockDraft(id)
	defer unlock()

	draft, err := c.store.Get(id)
	if err != nil {
		// ErrWorkingDraftNotFound propagates as itself: the draft is gone, and
		// no conversion can be reserved for it.
		return WorkingDraft{}, nil, err
	}
	if draft.State == workingDraftConverting {
		return c.resume(ctx, draft)
	}
	if draft.State != workingDraftEditing {
		return WorkingDraft{}, nil, fmt.Errorf("%w: draft is %s", ErrWorkingDraftConflict, draft.State)
	}
	if draft.Version != expectedVersion {
		return WorkingDraft{}, nil, fmt.Errorf("%w: expected version %d, have %d", ErrWorkingDraftConflict, expectedVersion, draft.Version)
	}

	// The request the server would create is read from the draft, never from
	// its caller.
	serverDerived, err := workingDraftFactoryRequest(draft, derivedFactoryRequestID(draft.ID, requestID))
	if err != nil {
		// Nothing is reserved, so the draft is untouched and still editing.
		return WorkingDraft{}, nil, err
	}
	resolved, currentSettings, err := c.factory.PrepareFrozen(serverDerived)
	if err != nil {
		return WorkingDraft{}, nil, err
	}
	if settingsVersion != "" && settingsVersion != currentSettings {
		return WorkingDraft{}, nil, fmt.Errorf("%w: project settings changed; review required", ErrWorkingDraftConflict)
	}
	if err := workingDraftCheckCallerRequest(request, resolved, serverDerived); err != nil {
		return WorkingDraft{}, nil, err
	}

	// The reserve is the linearization point: it compares the draft's version
	// and durably records the exact configuration and settings version this
	// conversion is bound to, so a later settings write cannot change what a
	// retry or a reconciliation creates. A replay of this request ID returns
	// the original reservation, whose frozen request is used below.
	reserved, err := c.store.BeginConversion(id, requestID, expectedVersion, resolved, currentSettings)
	if err != nil {
		return WorkingDraft{}, nil, err
	}
	frozen, reservedSettings := resolved, currentSettings
	if record, ok := c.store.conversionRecord(id); ok {
		if len(record.Request) > 0 {
			var recorded FactoryCreate
			if err := json.Unmarshal(record.Request, &recorded); err != nil {
				return reserved, nil, fmt.Errorf("%w: frozen factory request: %v", ErrWorkingDraftUncertain, err)
			}
			frozen = recorded
		}
		reservedSettings = record.SettingsVersion
	}

	// Recheck the settings version now that the intent is durable and the
	// draft store's lock is released. Nothing has been created yet, so a
	// settings write in this window is a refusal rather than an orphaned item:
	// the reservation is released back to editing and the next conversion is
	// reviewed against the settings that now apply. Leaving it reserved would
	// admit no save, delete, or conversion until the next restart.
	current, err := c.settings.ProjectSettingsETag(draft.ProjectRoot)
	if err != nil {
		return c.abandonReservation(id, requestID, "project settings unavailable; review the conversion again")
	}
	if current != reservedSettings {
		return c.abandonReservation(id, requestID, "project settings changed while reserving the conversion; review the conversion again")
	}
	return c.convertReserved(ctx, id, frozen, requestID)
}

// resume finishes a conversion the store reserved but never associated.
//
// A reservation is the linearization point: the draft's content is frozen while
// it is converting, so every caller asking to convert it is asking for the same
// reviewed handoff, and the frozen request — not the caller — decides what is
// created. Resuming is therefore the same path reconciliation takes, which is
// what makes a retry after a lost answer, a crash, or a factory that was
// briefly unavailable complete the one handoff instead of dead-ending until the
// next restart.
func (c *workingDraftConverter) resume(ctx context.Context, draft WorkingDraft) (WorkingDraft, *FactoryItem, error) {
	record, ok := c.store.conversionRecord(draft.ID)
	if !ok || len(record.Request) == 0 {
		// Converting without a reservation: there is nothing to replay, so the
		// draft is left for reconciliation rather than guessed at.
		return draft, nil, fmt.Errorf("%w: draft is %s with no reserved conversion", ErrWorkingDraftConflict, draft.State)
	}
	var frozen FactoryCreate
	if err := json.Unmarshal(record.Request, &frozen); err != nil {
		return draft, nil, fmt.Errorf("%w: frozen factory request: %v", ErrWorkingDraftUncertain, err)
	}
	return c.convertReserved(ctx, draft.ID, frozen, record.RequestID)
}

// abandonReservation releases a reservation no factory call was made for, which
// is only safe before the create: the draft returns to editing carrying the
// reason, so a browser can show a stale review, and the failure is reported as
// a conflict so the caller re-reads and reviews again instead of retrying bytes
// whose defaults have already moved.
func (c *workingDraftConverter) abandonReservation(id, requestID, reason string) (WorkingDraft, *FactoryItem, error) {
	failed, err := c.store.FailConversion(id, derivedFailID(id, requestID), reason)
	if err != nil {
		return c.currentDraft(id), nil, fmt.Errorf("%w: %v", ErrWorkingDraftUncertain, err)
	}
	return failed, nil, fmt.Errorf("%w: %s", ErrWorkingDraftConflict, reason)
}

// convertReserved runs the factory half of an already reserved conversion and
// associates the item. It is shared by Convert and Reconcile, so a conversion
// that is retried after a crash takes exactly the path a live one does.
func (c *workingDraftConverter) convertReserved(ctx context.Context, id string, frozen FactoryCreate, requestID string) (WorkingDraft, *FactoryItem, error) {
	// Only a live reservation may create. A conversion whose factory attempt was
	// already refused still has its reservation retained, but its draft is back
	// in editing: creating from that retained request would make an item no
	// association could ever reach, and the next conversion would make a second
	// one. The state is read under the per-draft lock, so it cannot move while
	// the create runs.
	current, err := c.store.Get(id)
	if err != nil {
		return WorkingDraft{}, nil, err
	}
	if current.State != workingDraftConverting {
		return current, nil, fmt.Errorf("%w: draft is %s", ErrWorkingDraftConflict, current.State)
	}
	if err := ctx.Err(); err != nil {
		// Starting a factory call the caller has already abandoned would make
		// the outcome unknowable, so the draft stays reserved instead.
		return current, nil, fmt.Errorf("%w: %v", ErrWorkingDraftUncertain, err)
	}
	item, err := c.factory.CreateFrozen(frozen, frozen)
	if err != nil {
		if workingDraftConfirmedFactoryError(err) {
			// The factory definitely created nothing, so the draft can be
			// edited again, carrying the reason, with a new version.
			failed, failErr := c.store.FailConversion(id, derivedFailID(id, requestID), workingDraftFailureText(err))
			if failErr != nil {
				return c.currentDraft(id), nil, fmt.Errorf("%w: %v", ErrWorkingDraftUncertain, errors.Join(err, failErr))
			}
			return failed, nil, err
		}
		return c.currentDraft(id), nil, fmt.Errorf("%w: %v", ErrWorkingDraftUncertain, err)
	}
	done, err := c.store.CompleteConversion(id, derivedDoneID(id, requestID), item.ID)
	if err != nil {
		// The item exists; only its association is missing. Reconciliation
		// replays the same frozen request and finds the same item.
		return c.currentDraft(id), nil, fmt.Errorf("%w: factory item %s created but not associated: %v", ErrWorkingDraftUncertain, item.ID, err)
	}
	return done, &item, nil
}

// workingDraftConfirmedFactoryError reports whether a factory failure proves no
// item was created: a validation or configuration failure is decided before
// anything is written, while an unavailable journal may have accepted the
// request and lost the response.
func workingDraftConfirmedFactoryError(err error) bool {
	return errors.Is(err, errFactoryInput) || errors.Is(err, errFactoryConfig)
}

// workingDraftFailureText is the reason a browser is shown for a refused
// conversion: the factory's own words, with a filesystem failure reduced to its
// operation and cause. The draft's error text is persisted and rendered, and an
// absolute path names the bridge's own directories, not anything the person
// editing the draft can act on.
func workingDraftFailureText(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && pathErr.Op != "" {
		return fmt.Sprintf("conversion failed: %s: %v", pathErr.Op, pathErr.Err)
	}
	return err.Error()
}

// currentDraft is the draft as the store holds it now, for a caller that must
// report an uncertain outcome without claiming a change.
func (c *workingDraftConverter) currentDraft(id string) WorkingDraft {
	d, err := c.store.Get(id)
	if err != nil {
		return WorkingDraft{ID: id}
	}
	return d
}

// Reconcile finishes every conversion a crash left reserved. It takes each
// draft's per-draft lock in turn, so it never races a live conversion of the
// same draft, and it runs before any new conversion is admitted.
//
// A draft is only advanced when the outcome is known: a confirmed factory
// refusal returns it to editing, a success associates its item, and anything
// else leaves it reserved for a later reconciliation and is reported as
// uncertain. Other drafts are still attempted, so one unreadable journal entry
// cannot block the rest.
func (c *workingDraftConverter) Reconcile(ctx context.Context) error {
	pending, err := c.store.convertingDrafts()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWorkingDraftUncertain, err)
	}
	var errs []error
	for _, record := range pending {
		unlock := c.lockDraft(record.ID)
		if err := c.reconcileOne(ctx, record); err != nil {
			errs = append(errs, err)
		}
		unlock()
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %v", ErrWorkingDraftUncertain, errors.Join(errs...))
	}
	return nil
}

func (c *workingDraftConverter) reconcileOne(ctx context.Context, record workingDraftConvertingDraft) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %v", record.ID, err)
	}
	var frozen FactoryCreate
	if err := json.Unmarshal(record.Request, &frozen); err != nil {
		return fmt.Errorf("%s: frozen factory request: %v", record.ID, err)
	}
	if _, _, err := c.convertReserved(ctx, record.ID, frozen, record.RequestID); err != nil {
		return fmt.Errorf("%s: %w", record.ID, err)
	}
	return nil
}

// workingDraftFactoryRequest is the server's own reading of a draft: the
// reviewed title, plan and spec, the draft's project, and every override the
// editor states. A blank override stays blank so the project's settings fill it
// on the way in exactly as they would for a handoff typed in FactoryNew, and a
// native draft's workspace override is dropped because a native run and a
// workspace are mutually exclusive — the same choice FactoryNew makes.
func workingDraftFactoryRequest(draft WorkingDraft, requestID string) (FactoryCreate, error) {
	content := draft.Content
	config := FactoryConfig{
		RepoID:                   content.RepoID,
		ProjectRoot:              draft.ProjectRoot,
		Native:                   content.Native,
		ImplementerAuto:          content.ImplementerAuto,
		NoRequiredCIAcknowledged: content.NoRequiredCIAcknowledged,
	}
	if !content.Native {
		config.Workspace = strings.TrimSpace(content.WorkspaceOverride)
	}
	config.TargetBranch = strings.TrimSpace(content.TargetBranchOverride)
	if profile := strings.TrimSpace(content.ImplementerOverride); profile != "" {
		config.ImplementerRouting = factoryProfileRouting(profile)
	}
	if profile := strings.TrimSpace(content.ReviewerOverride); profile != "" {
		config.ReviewerRouting = factoryProfileRouting(profile)
	}
	config.ReviewerInstructions = strings.TrimSpace(content.ReviewerInstructions)
	checks, err := workingDraftExpectedChecks(content.ChecksMode, content.ExpectedChecksText)
	if err != nil {
		return FactoryCreate{}, err
	}
	config.ExpectedChecks = checks
	config.MaxRounds = workingDraftLimit(content.MaxRoundsOverride)
	config.CIPollSeconds = workingDraftLimit(content.CIPollOverride)
	config.CIWaitSeconds = workingDraftLimit(content.CIWaitOverride)
	return FactoryCreate{RequestID: requestID, Title: content.Title, Plan: content.Plan, Spec: content.Spec, Config: config}, nil
}

// workingDraftExpectedChecks maps the editor's checks mode onto the factory
// configuration the same way FactoryNew's buildConfig does: inherit leaves the
// list nil so the project's defaults apply, none is a non-nil empty list (an
// explicit choice that survives resolution), and custom parses the canonical
// raw lines the handoff wrote: `check_run:<name>:<app id>` or
// `commit_status:<context>`.
//
// The split is where the specification puts it, because it is what lets a name
// or a context hold a colon of its own: the line's first colon names the kind,
// a commit status' context is the whole remainder of its line, and a check
// run's app id sits behind the line's last colon with the name in front of it.
// Splitting on every colon instead would refuse `commit_status:ci:lint` (a
// context of "ci:lint") and `check_run:Windows:Build:1234` (the name
// "Windows:Build"), both of which the handoff writes back out in exactly this
// form, while accepting `commit_status:ctx:` only by silently dropping its last
// colon. A line that does not parse is refused rather than dropped, so a
// conversion can never require fewer checks than the one that was reviewed.
//
// Whitespace is where the app's own parser decides, because a handoff it
// approves must not be refused here and a handoff it refuses must not be
// accepted here. parseExpectedChecks (app/lib/src/model/expected_checks.dart)
// trims the whole line and nothing else: the kind is the text before the first
// colon verbatim, so `check_run :b:1` has an unknown kind; a commit status'
// context is the entire remainder verbatim, so `commit_status: x` states " x";
// and a check run's name is verbatim while its app id goes through
// int.tryParse, which reads no surrounding space, so `check_run:b: 1` is not a
// number. Each of those is mirrored below rather than tidied up.
func workingDraftExpectedChecks(mode, text string) ([]FactoryCheckKey, error) {
	switch strings.TrimSpace(mode) {
	case "", workingDraftChecksInherit:
		return nil, nil
	case workingDraftChecksNone:
		return []FactoryCheckKey{}, nil
	case workingDraftChecksCustom:
	default:
		return nil, fmt.Errorf("%w: unknown checks mode %q", errFactoryInput, mode)
	}
	refuse := fmt.Errorf("%w: each expected check must be check_run:name:appId or commit_status:context", errFactoryInput)
	var checks []FactoryCheckKey
	for _, raw := range strings.Split(text, "\n") {
		// The whole line is the only thing trimmed: it is how a line's leading
		// and trailing whitespace stops counting, and how blank lines vanish.
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		// The first colon names the kind, verbatim; the rest of the line
		// belongs to the check and may hold colons of its own. Nothing after the
		// colon is trimmed on either side of the kind.
		colon := strings.Index(line, ":")
		if colon < 0 {
			return nil, refuse
		}
		kind, rest := line[:colon], line[colon+1:]
		switch kind {
		case "commit_status":
			// The context is the entire remainder, verbatim, so
			// `commit_status:ci:lint` states "ci:lint", `commit_status:ctx:`
			// keeps its last colon, and `commit_status: x` states " x". Only an
			// exactly empty context states no check.
			if rest == "" {
				return nil, refuse
			}
			checks = append(checks, FactoryCheckKey{Kind: kind, Name: rest})
			continue
		case "check_run":
			// The last colon separates the app id from a name that may itself
			// contain colons, as `check_run:Windows:Build:1234` does. The name
			// is taken verbatim — a name of " " is a name — and the app id is
			// read as written, so a space before it is not a number.
			idStart := strings.LastIndex(rest, ":")
			if idStart < 0 {
				return nil, refuse
			}
			name := rest[:idStart]
			id, err := strconv.ParseInt(rest[idStart+1:], 10, 64)
			if name == "" || err != nil || id <= 0 {
				return nil, refuse
			}
			checks = append(checks, FactoryCheckKey{Kind: kind, Name: name, AppID: id})
			continue
		}
		return nil, refuse
	}
	if len(checks) == 0 {
		return nil, fmt.Errorf("%w: enter at least one expected check, or choose no required checks", errFactoryInput)
	}
	return checks, nil
}

// workingDraftLimit reads an editor limit override. A blank or unparseable
// value stays zero, which means "inherit": a half-typed number is not a choice,
// and conversion is where the limits are validated.
func workingDraftLimit(text string) int {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0
	}
	return n
}

// workingDraftCheckCallerRequest refuses a factory request a caller claims but
// the server did not derive. A caller that sends nothing is accepting the
// server's reading; a caller that sends one must describe the same handoff,
// compared after normalization so that stating an inherited default explicitly
// is not a mismatch. Both accepted references are the server's own, and the
// request ID is never taken from the caller.
func workingDraftCheckCallerRequest(request, resolved, serverDerived FactoryCreate) error {
	if reflect.DeepEqual(request, FactoryCreate{}) {
		return nil
	}
	claimed, err := workingDraftRequestBytes(request)
	if err != nil {
		return errWorkingDraftTampered
	}
	for _, candidate := range []FactoryCreate{resolved, serverDerived} {
		want, err := workingDraftRequestBytes(candidate)
		if err != nil {
			continue
		}
		if bytes.Equal(claimed, want) {
			return nil
		}
	}
	return errWorkingDraftTampered
}

// workingDraftRequestBytes is a request's canonical comparison form: the
// request ID is dropped, because the server derives it, and normalization fills
// the same defaults on both sides so only a real difference survives.
func workingDraftRequestBytes(request FactoryCreate) ([]byte, error) {
	if err := factoryNormalize(&request); err != nil {
		return nil, err
	}
	// The request ID is derived by the server, so it is dropped after
	// normalization, which requires one: a caller can never claim it.
	request.RequestID = ""
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	return factoryCanonical(raw)
}
