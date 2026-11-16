package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// These tests are the proof that an approval or an answer acting on a person's
// instruction is distinguished from a model's or an external client's assertion
// of one. The trust anchor is the bridge: it mints an opaque, expiring,
// single-use receipt for exactly one operation, answers the attempt that has
// none with it, and treats a later attempt that presents it as the person's
// direction — in a header, never in a tool argument.
//
// Nothing here reaches a forge, a git remote, a container runtime or a model
// provider: the fixture's project, repository and journal are temp-dir state,
// and the clock each receipt is read against is pinned.

// marshalUserDirectionFixture is one bridge with a real factory journal, a
// registered project and repository, a project default that lets one built-in
// session create and approve, and the verified binding the control plane
// establishes — never one an argument can name.
type marshalUserDirectionFixture struct {
	*factoryMCPFixture
	binding MarshalSessionBinding
}

// newMarshalUserDirectionFixture builds that fixture under one project default.
func newMarshalUserDirectionFixture(t *testing.T, access MarshalFactoryAccess) *marshalUserDirectionFixture {
	t.Helper()
	fx := newFactoryMCPFixture(t, fullGrant())
	fixture := &marshalUserDirectionFixture{
		factoryMCPFixture: fx,
		binding: MarshalSessionBinding{
			InstanceID: "instance-1",
			ProjectID:  fx.projectID,
			SessionID:  "s1",
			Generation: "gen-1",
		},
	}
	fixture.setProjectAccess(t, access)
	return fixture
}

// projectAccessVersion is the version a project default is written against: the
// stored one, or the stable version of an absent one.
func (fx *marshalUserDirectionFixture) projectAccessVersion() string {
	if state, ok := fx.fleet.ws.marshalAccessDefault(fx.projectID); ok {
		return state.Version
	}
	return marshalAccessState(fx.projectID, "", "", MarshalFactoryAccess{}).Version
}

// setProjectAccess stores the project's built-in defaults the way an editor does:
// read the version, write against it.
func (fx *marshalUserDirectionFixture) setProjectAccess(t *testing.T, access MarshalFactoryAccess) {
	t.Helper()
	wrote, _, err := fx.srv.factoryAuth.putMarshalAccessDefault(fx.projectID, access, fx.projectAccessVersion())
	if err != nil || !wrote {
		t.Fatalf("put project access: wrote %v, err %v", wrote, err)
	}
}

// createMarshalDraft journals one draft for this fixture's verified session, the
// way the built-in create path does: authorize and append under the policy lock.
func (fx *marshalUserDirectionFixture) createMarshalDraft(t *testing.T, requestID string) FactoryItem {
	t.Helper()
	item, err := fx.srv.factoryAuth.WithAuthorizedMarshalMutation(fx.binding, "repo-1", factoryCapabilityCreateDraft,
		func(p factoryPrincipal) (FactoryItem, error) {
			// The draft is the fixture's usual proposal, so its configuration
			// carries the routing and CI acknowledgement an approval's
			// preflight requires; only its request identity is the built-in
			// one.
			req := fx.handoff(requestID)
			req.RequestID = p.scoped(requestID)
			p.RequestID = requestID
			return fx.store.CreateAuthorized(req, req, p, time.Now().UTC())
		})
	if err != nil {
		t.Fatalf("create draft %q: %v", requestID, err)
	}
	return item
}

// directionAccess is a project default that lets one built-in session queue work
// and answer questions under the given approval authority.
func directionAccess(authority string) MarshalFactoryAccess {
	yes := true
	return MarshalFactoryAccess{
		Read:              &yes,
		CreateDraft:       &yes,
		AnswerQuestion:    &yes,
		ApproveAndQueue:   &yes,
		ApprovalAuthority: &authority,
	}
}

// approveArgs is the approve_and_queue argument object for this fixture.
func (fx *marshalUserDirectionFixture) approveArgs(itemID, requestID string, version uint64, authority string) map[string]any {
	return map[string]any{
		"projectId": fx.projectID, "itemId": itemID, "requestId": requestID,
		"expectedVersion": version, "authority": authority,
	}
}

// callBuiltin calls one factory tool the way the built-in transport does: the
// verified principal travels on the request context, established by the control
// plane before the tool ran, and the only thing the caller adds is the receipt
// header. It reports the domain failure alongside the reply so a test can read
// the receipt a challenge carries.
func (fx *marshalUserDirectionFixture) callBuiltin(t *testing.T, receipt, tool string, args any) (factoryToolReply, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = withMCPPrincipal(req, mcpPrincipal{
		Client:  MCPClient{ID: marshalEnrollmentClientID(fx.binding)},
		Binding: fx.binding,
		Builtin: true,
	})
	if receipt != "" {
		req.Header.Set(MarshalUserDirectionHeader, receipt)
	}
	rec := httptest.NewRecorder()
	fx.srv.factoryToolCall(rec, req, MCPClient{ID: marshalEnrollmentClientID(fx.binding)}, json.RawMessage(`1`), tool, raw)

	var out rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: decode rpc response: %v (%s)", tool, err, rec.Body)
	}
	if out.Error != nil {
		t.Fatalf("%s: rpc error %d: %s", tool, out.Error.Code, out.Error.Message)
	}
	result, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatal(err)
	}
	var reply factoryToolReply
	if err := json.Unmarshal(result, &reply); err != nil {
		t.Fatalf("%s: decode tool result: %v (%s)", tool, err, result)
	}
	if len(reply.Content) != 1 || reply.Content[0].Type != "text" {
		t.Fatalf("%s: tool result has %d content blocks, want one text block: %s", tool, len(reply.Content), result)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(reply.Content[0].Text), &value); err != nil {
		t.Fatalf("%s: tool result text is not a JSON object: %v (%s)", tool, err, reply.Content[0].Text)
	}
	return reply, value
}

// challenge requires the failure to be a user-direction challenge and returns
// the receipt it carries, having checked the expiry it reports is a timestamp.
func challenge(t *testing.T, reply factoryToolReply, failure map[string]any) string {
	t.Helper()
	if !reply.IsError {
		t.Fatalf("the operation succeeded, want a user-direction challenge: %v", failure)
	}
	if code := failure["code"].(string); code != factoryCodeUserDirectionRequired {
		t.Fatalf("failure code = %q, want %q", code, factoryCodeUserDirectionRequired)
	}
	receipt, _ := failure["receipt"].(string)
	if receipt == "" {
		t.Fatalf("the challenge carries no receipt: %v", failure)
	}
	expiry, _ := failure["receiptExpiresAt"].(string)
	if _, err := time.Parse(time.RFC3339, expiry); err != nil {
		t.Fatalf("the challenge's expiry %q is not a timestamp: %v", expiry, err)
	}
	return receipt
}

// refused requires the failure to be the ordinary denial: no receipt, so no
// operation can be retried from it.
func refused(t *testing.T, reply factoryToolReply, failure map[string]any) {
	t.Helper()
	if !reply.IsError {
		t.Fatalf("the operation succeeded, want a denial: %v", failure)
	}
	if receipt, ok := failure["receipt"]; ok && receipt != "" {
		t.Fatalf("a denial carried a receipt: %v", failure)
	}
	if code := failure["code"].(string); code == factoryCodeUserDirectionRequired {
		t.Fatalf("a stale or unmatched receipt was answered with a fresh challenge: %v", failure)
	}
}

// lastProvenance is the attribution on one item's latest journal entry.
func lastProvenance(t *testing.T, fx *marshalUserDirectionFixture, itemID string) FactoryProvenance {
	t.Helper()
	item, err := fx.store.Get(itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.History) == 0 {
		t.Fatalf("item %s has no journal entry", itemID)
	}
	last := item.History[len(item.History)-1]
	if last.Provenance == nil {
		t.Fatalf("entry %q carries no provenance", last.Kind)
	}
	return *last.Provenance
}

// journalLength is how many entries one item's journal holds, which is what "no
// success was recorded" is asserted against.
func journalLength(t *testing.T, fx *marshalUserDirectionFixture, itemID string) int {
	t.Helper()
	item, err := fx.store.Get(itemID)
	if err != nil {
		t.Fatal(err)
	}
	return len(item.History)
}

// TestMarshalUserDirectionReceipt is the whole contract in one place: a
// user-directed built-in approval is refused with a receipt until that receipt
// is presented, presenting it admits exactly that operation and journals it as
// verified, it cannot be replayed, and standing authority — the project's own
// configured policy — needs no receipt at all.
func TestMarshalUserDirectionReceipt(t *testing.T) {
	fx := newMarshalUserDirectionFixture(t, directionAccess(factoryAuthorityUserDirected))
	item := fx.createMarshalDraft(t, "create-1")
	before := journalLength(t, fx, item.ID)

	reply, failure := fx.callBuiltin(t, "", factoryToolApproveAndQueue,
		fx.approveArgs(item.ID, "approve-1", item.Version, factoryAuthorityUserDirected))
	receipt := challenge(t, reply, failure)

	// A challenge is not an admission: nothing was queued and nothing was
	// journalled.
	stored, err := fx.store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Approvals) != 0 || stored.State == "queued" {
		t.Fatalf("a challenged approval was admitted: %+v", stored.State)
	}
	if got := journalLength(t, fx, item.ID); got != before {
		t.Fatalf("a challenged approval journalled %d entries, want %d", got-before, 0)
	}

	// Presenting it admits the exact operation the person confirmed.
	if receipt == "" {
		t.Fatal("no receipt to present")
	}
	reply, approved := fx.callBuiltin(t, receipt, factoryToolApproveAndQueue,
		fx.approveArgs(item.ID, "approve-1", item.Version, factoryAuthorityUserDirected))
	if reply.IsError {
		t.Fatalf("the confirmed approval failed: %v", approved)
	}
	if approved["state"] != "queued" {
		t.Fatalf("the confirmed approval returned %v", approved)
	}
	provenance := lastProvenance(t, fx, item.ID)
	if provenance.AuthorityOrigin != factoryAuthorityOriginVerifiedUserDirected {
		t.Fatalf("authority origin = %q, want %q", provenance.AuthorityOrigin, factoryAuthorityOriginVerifiedUserDirected)
	}
	if provenance.Authority != factoryAuthorityUserDirected ||
		provenance.Action != factoryCapabilityApproveAndQueue ||
		provenance.SessionID != fx.binding.SessionID ||
		provenance.InstanceID != fx.binding.InstanceID ||
		provenance.ClientID != "" {
		t.Fatalf("verified approval provenance = %+v", provenance)
	}

	// One confirmation authorizes one attempt: replaying the receipt is a
	// denial, and it journals nothing.
	queued := journalLength(t, fx, item.ID)
	reply, failure = fx.callBuiltin(t, receipt, factoryToolApproveAndQueue,
		fx.approveArgs(item.ID, "approve-1", item.Version, factoryAuthorityUserDirected))
	refused(t, reply, failure)
	if got := journalLength(t, fx, item.ID); got != queued {
		t.Fatalf("a replayed receipt journalled %d entries", got-queued)
	}

	// Standing authority is what the store holds, configured separately, so it
	// owes no confirmation and is journalled as standing.
	standing := newMarshalUserDirectionFixture(t, directionAccess(factoryAuthorityStanding))
	standingItem := standing.createMarshalDraft(t, "create-standing")
	reply, approved = standing.callBuiltin(t, "", factoryToolApproveAndQueue,
		standing.approveArgs(standingItem.ID, "approve-standing", standingItem.Version, factoryAuthorityStanding))
	if reply.IsError {
		t.Fatalf("a standing approval needed a confirmation: %v", approved)
	}
	provenance = lastProvenance(t, standing, standingItem.ID)
	if provenance.AuthorityOrigin != factoryAuthorityOriginStanding {
		t.Fatalf("standing authority origin = %q, want %q", provenance.AuthorityOrigin, factoryAuthorityOriginStanding)
	}
	if n := standing.srv.factoryAuth.pendingUserDirections(); n != 0 {
		t.Fatalf("standing authority minted %d receipts, want none", n)
	}
}

// TestMarshalUserDirectionModelAssertion proves a model's claim is not evidence.
// A built-in call that writes "authority": "user_directed" into its arguments
// gets the challenge, never an admission, and a receipt written into those
// arguments — even a real one — is refused: the tools read a receipt from the
// header alone.
func TestMarshalUserDirectionModelAssertion(t *testing.T) {
	fx := newMarshalUserDirectionFixture(t, directionAccess(factoryAuthorityUserDirected))
	item := fx.createMarshalDraft(t, "create-assert")
	before := journalLength(t, fx, item.ID)

	// The claim, and nothing else.
	reply, failure := fx.callBuiltin(t, "", factoryToolApproveAndQueue,
		fx.approveArgs(item.ID, "approve-claim", item.Version, factoryAuthorityUserDirected))
	receipt := challenge(t, reply, failure)
	if got := journalLength(t, fx, item.ID); got != before {
		t.Fatalf("a claimed authority journalled %d entries", got-before)
	}
	// The last entry is still the draft's own creation, whose origin is the
	// store's standing policy. What must not exist is an approval entry: the
	// claim journalled nothing, so no entry carries the claimed direction.
	if provenance := lastProvenance(t, fx, item.ID); provenance.Action == factoryCapabilityApproveAndQueue {
		t.Fatalf("a claimed authority was journalled as an approval: %+v", provenance)
	}

	// The same operation with the receipt in the JSON body and no header: the
	// argument is an unknown field, so the request is refused outright and the
	// receipt stays unspent.
	args := fx.approveArgs(item.ID, "approve-claim", item.Version, factoryAuthorityUserDirected)
	args["receipt"] = receipt
	reply, failure = fx.callBuiltin(t, "", factoryToolApproveAndQueue, args)
	if !reply.IsError || failure["code"] != factoryCodeInvalidInput {
		t.Fatalf("a receipt in the arguments = %v, %v; want %q", reply.IsError, failure, factoryCodeInvalidInput)
	}
	if got := journalLength(t, fx, item.ID); got != before {
		t.Fatalf("an argument-supplied receipt journalled %d entries", got-before)
	}

	// The header is the one carrier: the same receipt admits the operation.
	reply, approved := fx.callBuiltin(t, receipt, factoryToolApproveAndQueue,
		fx.approveArgs(item.ID, "approve-claim", item.Version, factoryAuthorityUserDirected))
	if reply.IsError {
		t.Fatalf("the header-supplied receipt was refused: %v", approved)
	}
}

// TestMarshalUserDirectionNoDuplicateConfirmation checks that one operation owes
// one confirmation: a retry of the same operation before the receipt is used
// hands back the receipt already minted, and the confirmation that consumes it
// leaves nothing pending.
func TestMarshalUserDirectionNoDuplicateConfirmation(t *testing.T) {
	fx := newMarshalUserDirectionFixture(t, directionAccess(factoryAuthorityUserDirected))
	item := fx.createMarshalDraft(t, "create-twice")
	args := fx.approveArgs(item.ID, "approve-twice", item.Version, factoryAuthorityUserDirected)

	reply, failure := fx.callBuiltin(t, "", factoryToolApproveAndQueue, args)
	first := challenge(t, reply, failure)
	if n := fx.srv.factoryAuth.pendingUserDirections(); n != 1 {
		t.Fatalf("the bridge holds %d receipts after one confirmation was asked for, want 1", n)
	}

	reply, failure = fx.callBuiltin(t, "", factoryToolApproveAndQueue, args)
	second := challenge(t, reply, failure)
	if second != first {
		t.Fatalf("a second confirmation was requested for one operation: %q then %q", first, second)
	}
	if n := fx.srv.factoryAuth.pendingUserDirections(); n != 1 {
		t.Fatalf("a retry minted a second receipt: %d pending", n)
	}

	reply, approved := fx.callBuiltin(t, first, factoryToolApproveAndQueue, args)
	if reply.IsError {
		t.Fatalf("the confirmed approval failed: %v", approved)
	}
	if n := fx.srv.factoryAuth.pendingUserDirections(); n != 0 {
		t.Fatalf("%d receipts are still pending after the operation was admitted", n)
	}
}

// TestMarshalUserDirectionWrongTarget proves a receipt is bound to the exact
// operation it was minted for: another item, or another request, matches
// nothing, and a mismatch never consumes it — the operation it was minted for
// still succeeds.
func TestMarshalUserDirectionWrongTarget(t *testing.T) {
	fx := newMarshalUserDirectionFixture(t, directionAccess(factoryAuthorityUserDirected))
	item := fx.createMarshalDraft(t, "create-a")
	other := fx.createMarshalDraft(t, "create-b")
	before := journalLength(t, fx, other.ID)

	reply, failure := fx.callBuiltin(t, "", factoryToolApproveAndQueue,
		fx.approveArgs(item.ID, "approve-a", item.Version, factoryAuthorityUserDirected))
	receipt := challenge(t, reply, failure)

	// The receipt names item a's operation; presenting it for item b is a
	// denial, and item b journals nothing.
	reply, failure = fx.callBuiltin(t, receipt, factoryToolApproveAndQueue,
		fx.approveArgs(other.ID, "approve-a", other.Version, factoryAuthorityUserDirected))
	refused(t, reply, failure)
	if got := journalLength(t, fx, other.ID); got != before {
		t.Fatalf("a receipt presented for another item journalled %d entries", got-before)
	}

	// Nor does it cover a different request for the same item.
	reply, failure = fx.callBuiltin(t, receipt, factoryToolApproveAndQueue,
		fx.approveArgs(item.ID, "approve-other", item.Version, factoryAuthorityUserDirected))
	refused(t, reply, failure)

	// The mismatches did not consume it: the operation it was minted for is
	// still the one it admits.
	reply, approved := fx.callBuiltin(t, receipt, factoryToolApproveAndQueue,
		fx.approveArgs(item.ID, "approve-a", item.Version, factoryAuthorityUserDirected))
	if reply.IsError {
		t.Fatalf("the receipt's own operation was refused: %v", approved)
	}
}

// TestMarshalUserDirectionExternalClientAssertion keeps an external MCP client
// exactly where it was: its claim matching its configured grant is admitted with
// no receipt and no new failure, and where the server records where that
// authority came from it says the client asserted it.
func TestMarshalUserDirectionExternalClientAssertion(t *testing.T) {
	grant := fullGrant()
	grant.ApprovalAuthority = factoryAuthorityUserDirected
	f := newMCPFactoryToolFixture(t, grant)

	created := factoryToolOK(t, f.srv, f.token, factoryToolCreateDraft, f.proposal("create-external"))
	itemID := created["itemId"].(string)

	// No receipt, no header, no challenge: an external client's admission is
	// unchanged, and the approval's own provenance is byte for byte what it was.
	approved := factoryToolOK(t, f.srv, f.token, factoryToolApproveAndQueue,
		f.approve(itemID, "approve-external", 1, factoryAuthorityUserDirected))
	if approved["state"] != "queued" {
		t.Fatalf("an external approval returned %v", approved)
	}
	item, err := f.store.Get(itemID)
	if err != nil {
		t.Fatal(err)
	}
	approvalProvenance := *item.History[len(item.History)-1].Provenance
	want := FactoryProvenance{
		ClientID: f.clientID, ProjectID: f.projectID,
		Action: factoryCapabilityApproveAndQueue, RequestID: "approve-external",
		Authority: factoryAuthorityUserDirected,
	}
	if approvalProvenance != want {
		t.Fatalf("external approval provenance = %+v, want %+v", approvalProvenance, want)
	}
	if n := f.srv.factoryAuth.pendingUserDirections(); n != 0 {
		t.Fatalf("an external client was asked for %d confirmations", n)
	}

	// The answer carries a person's instruction the server cannot verify. It is
	// admitted exactly as before and its entry keeps the bytes it always had: an
	// external entry is distinguished by naming a client id and claiming no
	// origin, never by a new field. No origin is stamped, so this stays a client
	// assertion rather than becoming a verified user direction.
	version := f.seedQuestion(t, itemID)
	answered := factoryToolOK(t, f.srv, f.token, factoryToolAnswerQuestion,
		f.answer(itemID, "answer-external", version, "decision-1", factoryAuthorityUserDirected))
	if answered["state"] != "repairing" {
		t.Fatalf("an external answer returned %v", answered)
	}
	item, err = f.store.Get(itemID)
	if err != nil {
		t.Fatal(err)
	}
	answerProvenance := *item.History[len(item.History)-1].Provenance
	if answerProvenance.Authority != factoryAuthorityUserDirected ||
		answerProvenance.AuthorityOrigin != "" ||
		answerProvenance.SessionID != "" ||
		answerProvenance.ClientID != f.clientID {
		t.Fatalf("external answer provenance = %+v", answerProvenance)
	}
}

// TestMarshalUserDirectionRevoked covers both ways a receipt stops being
// evidence: it expired, or the authority that made the operation admissible was
// taken away. Neither journals a success.
func TestMarshalUserDirectionRevoked(t *testing.T) {
	fx := newMarshalUserDirectionFixture(t, directionAccess(factoryAuthorityUserDirected))
	base := time.Unix(1_700_000_000, 0).UTC()
	fx.srv.factoryAuth.now = func() time.Time { return base }

	item := fx.createMarshalDraft(t, "create-revoke")
	before := journalLength(t, fx, item.ID)
	args := fx.approveArgs(item.ID, "approve-revoke", item.Version, factoryAuthorityUserDirected)

	reply, failure := fx.callBuiltin(t, "", factoryToolApproveAndQueue, args)
	receipt := challenge(t, reply, failure)

	// One second past the receipt's life it is not evidence any more.
	fx.srv.factoryAuth.now = func() time.Time { return base.Add(marshalUserDirectionTTL + time.Second) }
	reply, failure = fx.callBuiltin(t, receipt, factoryToolApproveAndQueue, args)
	refused(t, reply, failure)
	if got := journalLength(t, fx, item.ID); got != before {
		t.Fatalf("an expired receipt journalled %d entries", got-before)
	}

	// A fresh confirmation, then the authority it needs is revoked: the receipt
	// is intact and still admits nothing.
	fx.srv.factoryAuth.now = func() time.Time { return base }
	reply, failure = fx.callBuiltin(t, "", factoryToolApproveAndQueue, args)
	fresh := challenge(t, reply, failure)
	if fresh == receipt {
		t.Fatal("an expired receipt was handed back as a live one")
	}
	fx.setProjectAccess(t, MarshalFactoryAccess{Read: boolPointer(true)})

	reply, failure = fx.callBuiltin(t, fresh, factoryToolApproveAndQueue, args)
	refused(t, reply, failure)
	if got := journalLength(t, fx, item.ID); got != before {
		t.Fatalf("a receipt presented after revocation journalled %d entries", got-before)
	}
	stored, err := fx.store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State == "queued" || len(stored.Approvals) != 0 {
		t.Fatalf("a revoked operation was admitted: %+v", stored.State)
	}
}

// boolPointer names one capability value in a stored access record.
func boolPointer(b bool) *bool { return &b }
