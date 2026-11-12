package bridge

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strings"
)

// This file assembles the discovery report from the three sources Tasks 1-3
// provide: the frozen contract, the read-only forge probes, and the read-only
// checkout inspection.
//
// Discovery is observational. Nothing here writes to the checkout or the
// workspace, creates a project or an agent, or persists a report. The only
// subprocesses are the allowlisted local Git reads in discoveryGitState; the
// only network calls are the forge probes, and only for a repository whose
// forge is declared.

// Report statuses. "unavailable" is the declared-forge outage state that stays
// visible instead of silently degrading to Git-only.
const (
	discoveryStatusOK          = "ok"
	discoveryStatusDegraded    = "degraded"
	discoveryStatusUnavailable = "unavailable"
)

// Forge-side sections, as they appear in a fact's or gap's section. Both CI
// capability keys share the "ci" section, exactly as the report contract's
// example gap does; the checkout's own CI-definition section is also "ci".
const (
	discoverySectionRepository   = "repository"
	discoverySectionBranches     = "branches"
	discoverySectionPullRequests = "pull_requests"
)

// discoveryRecommendedOrigin marks an advisory suggestion, as opposed to a
// detected fact. Discovery never applies one.
const discoveryRecommendedOrigin = "recommended"

// The five forge capability keys with the section each reports under.
var discoveryForgeSections = []struct {
	key     string
	section string
}{
	{"repository_metadata", discoverySectionRepository},
	{"branches", discoverySectionBranches},
	{"pull_requests", discoverySectionPullRequests},
	{"ci_checks", discoverySectionCI},
	{"ci_logs", discoverySectionCI},
}

// The five checkout capability keys with the section Task 3 reports them
// under.
var discoveryCheckoutSections = []struct {
	key     string
	section string
}{
	{discoveryKeyInstructions, discoverySectionInstructions},
	{discoveryKeyDocs, discoverySectionDocs},
	{discoveryKeyTooling, discoverySectionTooling},
	{discoveryKeyEnvironment, discoverySectionEnvironment},
	{discoveryKeyCI, discoverySectionCI},
}

// DiscoverRepo assembles the read-only discovery report for a registered
// repository. It resolves a credential only for a declared forge, reads the
// checkout only through the project linked to the repository, and never turns
// a declared-forge failure into the Git-only path.
//
// The returned error is reserved for a request the bridge cannot assemble at
// all (an unregistered repository id). A forge that cannot answer is a report
// whose status and error say so, because the checkout facts remain useful.
func (f *Fleet) DiscoverRepo(ctx context.Context, repo Repo) (*DiscoveryReport, error) {
	return f.discoverRepo(ctx, repo, nil)
}

// discoverRepo is the testable form of DiscoverRepo: client overrides the
// shared forge client so a test can observe whether any request happens.
func (f *Fleet) discoverRepo(ctx context.Context, repo Repo, client *http.Client) (*DiscoveryReport, error) {
	if repo.ID == "" {
		return nil, fmt.Errorf("bridge: discovery needs a registered repository id")
	}
	a := newDiscoveryAssembler(repo)
	a.inspectCheckout(ctx, f)
	a.probeForge(ctx, f, client)
	a.recommend()
	return a.report(), nil
}

// discoveryAssembler collects one report. Capabilities are keyed by capability
// key so a section can never be reported twice or omitted, and every one of
// the frozen ten starts in a safe non-supported state.
type discoveryAssembler struct {
	repo      Repo
	caps      map[string]DiscoveryCapability
	gaps      map[string]DiscoveryGap
	facts     []DiscoveryFact
	recs      []DiscoveryRecommendation
	worktree  *DiscoveryWorktree
	outage    string
	unavail   bool
	forgeHead string
	forgeBase string

	checkoutHead   string
	checkoutBranch string
}

// newDiscoveryAssembler seeds all ten capability keys. A key a code path never
// overwrites stays visibly unsupported rather than vanishing from the wire.
func newDiscoveryAssembler(repo Repo) *discoveryAssembler {
	a := &discoveryAssembler{
		repo: repo,
		caps: make(map[string]DiscoveryCapability, len(discoveryCapabilityOrder)),
		gaps: make(map[string]DiscoveryGap, len(discoveryCapabilityOrder)),
	}
	for _, key := range discoveryCapabilityOrder {
		a.caps[key] = DiscoveryCapability{
			Key:    key,
			State:  DiscoveryCapabilityUnsupported,
			Detail: "discovery did not probe this capability",
		}
	}
	return a
}

// set records one capability.
func (a *discoveryAssembler) set(key string, state DiscoveryCapabilityState, detail string) {
	a.caps[key] = DiscoveryCapability{Key: key, State: state, Detail: detail}
}

// degrade records a capability that is not supported and the gap explaining
// what would fix it.
func (a *discoveryAssembler) degrade(key, section string, state DiscoveryCapabilityState, detail string) {
	a.set(key, state, detail)
	a.addGap(key, section, state, discoveryGapDetail(detail, key, state))
}

// addGap records one gap per capability key: the plan allows one gap per
// degraded section, so a second failure in the same section folds into the
// first instead of duplicating it.
func (a *discoveryAssembler) addGap(key, section string, state DiscoveryCapabilityState, detail string) {
	if _, ok := a.gaps[key]; ok {
		return
	}
	a.gaps[key] = DiscoveryGap{Key: key, Section: section, State: state, Detail: detail}
}

// addForgeFact records one detected forge fact. Its revision is stamped once
// the observed default-branch head is known.
func (a *discoveryAssembler) addForgeFact(section, kind, value, source, detail string, size int64) {
	a.facts = append(a.facts, DiscoveryFact{
		Origin:  discoveryDetectedOrigin,
		Section: section,
		Kind:    kind,
		Value:   value,
		Source:  source,
		Size:    size,
		Detail:  detail,
	})
}

// state reports one capability's state, for the recommendation pass.
func (a *discoveryAssembler) state(key string) DiscoveryCapabilityState {
	return a.caps[key].State
}

// inspectCheckout reads the checkout linked to the repository, and its local
// Git state, without changing either.
func (a *discoveryAssembler) inspectCheckout(ctx context.Context, f *Fleet) {
	root := ""
	if f != nil && f.ws != nil {
		// The linked project is the only checkout source. Never repo.URL, and
		// never a clone or a mirror.
		root = f.ws.ProjectRootByRepo(a.repo.ID)
	}
	if root == "" {
		a.degradeCheckout(DiscoveryCapabilityUnconfigured,
			"no Marshal project links this repository, so there is no checkout to inspect")
		return
	}

	findings, err := inspectCheckout(root)
	if err != nil {
		state := DiscoveryCapabilityUnconfigured
		if errors.Is(err, fs.ErrPermission) {
			state = DiscoveryCapabilityInsufficientPermission
		}
		a.degradeCheckout(state, fmt.Sprintf("the linked checkout %s could not be inspected: %s", root, firstErrorLine(err)))
		a.worktree = &DiscoveryWorktree{Path: root, Detail: "the linked checkout could not be inspected"}
		return
	}

	a.worktree = &DiscoveryWorktree{Path: root}
	if f != nil {
		head, branch, dirty, files, detail, gitErr := f.discoveryGitState(ctx, root)
		if gitErr != nil {
			// The files were still inspected. A checkout that is not a Git
			// repository leaves the revision undetermined rather than absent.
			a.worktree.Detail = "git state could not be read: " + firstErrorLine(gitErr)
		} else {
			a.worktree.Head, a.worktree.Branch = head, branch
			a.worktree.Dirty, a.worktree.DirtyFiles = dirty, files
			a.worktree.Detail = detail
			a.checkoutHead, a.checkoutBranch = head, branch
		}
	} else {
		a.worktree.Detail = "git state could not be read: no git runner is available"
	}

	for _, fact := range findings.facts() {
		// Checkout facts carry the checkout HEAD they were read at.
		fact.Revision = a.checkoutHead
		a.facts = append(a.facts, fact)
	}
	for _, section := range discoveryCheckoutSections {
		gaps := checkoutGapsForKey(findings, section.key)
		if len(gaps) == 0 {
			a.set(section.key, DiscoveryCapabilitySupported, "")
			continue
		}
		state := discoveryWorstState(gaps)
		summary := discoveryJoinDetails(gaps)
		a.degrade(section.key, section.section, state, summary)
	}
}

// degradeCheckout marks every checkout capability with one shared state and
// reason: with no readable checkout, none of the five could be inspected.
func (a *discoveryAssembler) degradeCheckout(state DiscoveryCapabilityState, detail string) {
	for _, section := range discoveryCheckoutSections {
		a.degrade(section.key, section.section, state, detail)
	}
}

// checkoutGapsForKey returns the inspection gaps belonging to one capability.
func checkoutGapsForKey(findings checkoutFindings, key string) []DiscoveryGap {
	gaps := make([]DiscoveryGap, 0, len(findings.Gaps))
	for _, gap := range findings.Gaps {
		if gap.Key == key {
			gaps = append(gaps, gap)
		}
	}
	return gaps
}

// probeForge probes each forge section independently. A failed section
// degrades only itself; a repository with no declared forge makes no request
// at all.
func (a *discoveryAssembler) probeForge(ctx context.Context, f *Fleet, client *http.Client) {
	repo := a.repo
	if repo.Forge == "" {
		// The sanctioned Git-only path: no adapter is engaged, which is
		// "unsupported", not a failure to hide.
		a.degradeForgeSections(DiscoveryCapabilityUnsupported,
			"no forge is declared for this repository")
		return
	}

	probe, err := DiscoveryForgeFor(repo, client)
	if err != nil {
		a.degradeForgeSections(DiscoveryCapabilityUnsupported,
			fmt.Sprintf("forge %q has no read-only discovery adapter in this bridge", repo.Forge))
		return
	}

	cred, credErr := f.discoveryCredential(ctx, repo)
	if credErr != nil {
		// An absent, non-PAT, invalid or expired credential is unconfigured,
		// and no request is attempted with it.
		a.degradeForgeSections(DiscoveryCapabilityUnconfigured,
			"no usable personal access token credential for this repository: "+credErr.Error())
		return
	}

	meta, metaErr := probe.RepositoryMetadata(ctx, repo, cred)
	metadataReadable := metaErr == nil
	if metaErr != nil {
		state, detail := discoveryProbeOutcome(metaErr, false)
		a.degrade("repository_metadata", discoverySectionRepository, state, detail)
		switch state {
		case DiscoveryCapabilityTemporarilyUnavailable:
			// A declared supported forge that cannot answer at all is a
			// visible outage, not a silent downgrade: the remaining forge
			// probes cannot succeed either.
			a.unavail = true
			a.outage = fmt.Sprintf(
				"the declared %s forge could not be reached while reading repository metadata; checkout facts are still reported",
				repo.Forge)
			a.degradeForgeSections(state,
				"the forge could not be reached while reading repository metadata")
			return
		case DiscoveryCapabilityUnconfigured:
			// An unusable instance setting or credential is instance-wide, so
			// every forge section shares it.
			a.degradeForgeSections(state, detail)
			return
		}
	} else {
		a.set("repository_metadata", DiscoveryCapabilitySupported, "")
		a.forgeBase = meta.DefaultBranch
		a.addMetadataFacts(meta)
	}

	branches, branchErr := probe.Branches(ctx, repo, cred)
	if branchErr != nil {
		state, detail := discoveryProbeOutcome(branchErr, metadataReadable)
		a.degrade("branches", discoverySectionBranches, state, detail)
	} else {
		a.set("branches", DiscoveryCapabilitySupported, "")
		for _, branch := range branches {
			detail := ""
			if branch.SHA != "" {
				detail = "head " + branch.SHA
			}
			a.addForgeFact(discoverySectionBranches, "branch", branch.Name, "forge:branches", detail, 0)
		}
		if len(branches) == 0 {
			a.addForgeFact(discoverySectionBranches, "branch", discoveryAbsenceValue, "forge:branches",
				"the forge reported no branches", 0)
		}
		a.forgeHead = branchHead(branches, meta.DefaultBranch, repo.Branch)
	}

	pulls, pullErr := probe.PullRequests(ctx, repo, cred)
	if pullErr != nil {
		state, detail := discoveryProbeOutcome(pullErr, metadataReadable)
		a.degrade("pull_requests", discoverySectionPullRequests, state, detail)
	} else {
		a.set("pull_requests", DiscoveryCapabilitySupported, "")
		for _, pull := range pulls {
			detail := "state " + pull.State
			if pull.HeadSHA != "" {
				detail += "; head " + pull.HeadSHA
			}
			a.addForgeFact(discoverySectionPullRequests, "pull_request",
				fmt.Sprintf("#%d %s", pull.Number, pull.Title), "forge:pulls", detail, 0)
		}
		if len(pulls) == 0 {
			a.addForgeFact(discoverySectionPullRequests, "pull_request", discoveryAbsenceValue,
				"forge:pulls", "the forge reported no open pull requests", 0)
		}
	}

	a.probeChecks(ctx, probe, cred, metadataReadable, metadataReadable && meta.DefaultBranch != "")
}

// probeChecks probes CI checks and, only for a readable failing check, its
// detailed log. Unknown refs and unreadable checks are "not exercised", not a
// second failure.
func (a *discoveryAssembler) probeChecks(
	ctx context.Context, probe DiscoveryForge, cred Credential,
	metadataReadable, haveDefaultBranch bool,
) {
	repo := a.repo
	ref := a.forgeHead
	if ref == "" && metadataReadable && haveDefaultBranch {
		ref = a.forgeBase
	}
	if ref == "" {
		ref = repo.Branch
	}
	if ref == "" {
		const notExercised = "not exercised: no default branch revision is known to check"
		a.set("ci_checks", DiscoveryCapabilitySupported, notExercised)
		a.set("ci_logs", DiscoveryCapabilitySupported, notExercised)
		return
	}

	checks, checkErr := probe.Checks(ctx, repo, ref, cred)
	if checkErr != nil {
		state, detail := discoveryProbeOutcome(checkErr, metadataReadable)
		a.degrade("ci_checks", discoverySectionCI, state, detail)
		a.set("ci_logs", DiscoveryCapabilitySupported,
			"not exercised: the CI checks section could not be read")
		return
	}
	a.set("ci_checks", DiscoveryCapabilitySupported, "")
	for _, check := range checks {
		a.addForgeFact(discoverySectionCI, "check", check.Name, "forge:checks", checkDetail(check), 0)
	}
	if len(checks) == 0 {
		a.addForgeFact(discoverySectionCI, "check", discoveryAbsenceValue, "forge:checks",
			"the forge reported no checks on "+ref, 0)
	}

	failing := failingChecks(checks)
	if len(failing) == 0 {
		a.set("ci_logs", DiscoveryCapabilitySupported, "no failing check to exercise")
		return
	}
	log, logErr := probe.CheckLogs(ctx, repo, failing[0], cred)
	if logErr != nil {
		state, detail := discoveryProbeOutcome(logErr, metadataReadable)
		a.degrade("ci_logs", discoverySectionCI, state, detail)
		return
	}
	a.set("ci_logs", DiscoveryCapabilitySupported, "detailed logs readable for "+failing[0].Name)
	a.addForgeFact(discoverySectionCI, "check_log", failing[0].Name, "forge:logs",
		fmt.Sprintf("bounded detail readable (%d bytes)", len(log)), int64(len(log)))
}

// degradeForgeSections marks all five forge capabilities with one shared
// state and reason, for the paths where the whole forge is unusable.
func (a *discoveryAssembler) degradeForgeSections(state DiscoveryCapabilityState, detail string) {
	for _, section := range discoveryForgeSections {
		a.degrade(section.key, section.section, state, detail)
	}
}

// addMetadataFacts records the repository-level metadata facts.
func (a *discoveryAssembler) addMetadataFacts(meta DiscoveryRepositoryMetadata) {
	add := func(kind, value, detail string) {
		if value == "" {
			return
		}
		a.addForgeFact(discoverySectionRepository, kind, value, "forge:repository", detail, 0)
	}
	add("default_branch", meta.DefaultBranch, "")
	add("visibility", meta.Visibility, "")
	add("description", meta.Description, "")
	add("language", meta.Language, "")
	for _, topic := range meta.Topics {
		add("topic", topic, "")
	}
	if meta.SizeBytes > 0 {
		a.addForgeFact(discoverySectionRepository, "size", fmt.Sprintf("%d", meta.SizeBytes),
			"forge:repository", "reported by the forge", meta.SizeBytes)
	}
}

// discoveryCredential resolves the repository's credential for a declared
// forge. It is never called for a repository with no forge, so the Git-only
// path cannot touch a credential or a host.
func (f *Fleet) discoveryCredential(ctx context.Context, repo Repo) (Credential, error) {
	if f == nil || f.creds == nil {
		return Credential{}, errors.New("the bridge has no credential store")
	}
	cred, err := f.creds.Resolve(ctx, repo.OwnerID, repo.CredRef)
	if err != nil {
		return Credential{}, err
	}
	if cred.Kind != "pat" || cred.literal == "" {
		// An SSH or none credential cannot authenticate an API probe, and its
		// secret (a key path) is not what the forge endpoints accept.
		return Credential{}, fmt.Errorf(
			"the repository's credential is kind %q, not a personal access token", cred.Kind)
	}
	return cred, nil
}

// discoveryProbeOutcome classifies one probe failure. Classification is the
// frozen contextual one; the adapter's own message wins for the sentinels it
// raises, because that message names the setting the operator must fix.
func discoveryProbeOutcome(err error, metadataReadable bool) (DiscoveryCapabilityState, string) {
	state, detail := discoveryCapabilityState(err, metadataReadable)
	if err == nil {
		return state, detail
	}
	for _, sentinel := range []error{errDiscoveryUnconfigured, errDiscoveryUnsupported} {
		if !errors.Is(err, sentinel) {
			continue
		}
		if msg := strings.TrimSuffix(err.Error(), ": "+sentinel.Error()); msg != "" && msg != err.Error() {
			detail = msg
		}
		break
	}
	return state, detail
}

// discoveryFix names what would resolve a capability in this state. It is
// appended to a gap's detail, never to a supported capability's.
func discoveryFix(key string, state DiscoveryCapabilityState) string {
	switch state {
	case DiscoveryCapabilityUnconfigured:
		return "configure this capability for the repository and retry"
	case DiscoveryCapabilityInsufficientPermission:
		return "grant the credential or process read access and retry"
	case DiscoveryCapabilityTemporarilyUnavailable:
		return "retry later"
	case DiscoveryCapabilityUnsupported:
		if strings.HasPrefix(key, "checkout_") {
			return "replace the target with a regular file this adapter reads"
		}
		return "declare github or gitea for this repository"
	}
	return ""
}

// discoveryGapDetail joins a cause with the fix that would remove it.
func discoveryGapDetail(detail, key string, state DiscoveryCapabilityState) string {
	fix := discoveryFix(key, state)
	switch {
	case fix == "":
		return detail
	case strings.Contains(detail, fix):
		return detail
	case detail == "":
		return fix
	}
	return detail + "; " + fix
}

// discoveryStateSeverity orders states by how specific the fix is, so a
// section with several failed targets reports the most actionable one.
func discoveryStateSeverity(state DiscoveryCapabilityState) int {
	switch state {
	case DiscoveryCapabilityUnconfigured:
		return 5
	case DiscoveryCapabilityInsufficientPermission:
		return 4
	case DiscoveryCapabilityTemporarilyUnavailable:
		return 3
	case DiscoveryCapabilityUnsupported:
		return 2
	}
	return 1
}

// discoveryWorstState returns the most actionable state among gaps.
func discoveryWorstState(gaps []DiscoveryGap) DiscoveryCapabilityState {
	worst := DiscoveryCapabilitySupported
	for _, gap := range gaps {
		if discoveryStateSeverity(gap.State) > discoveryStateSeverity(worst) {
			worst = gap.State
		}
	}
	return worst
}

// discoveryJoinDetails joins gap details in a stable order, so the same
// checkout always produces the same capability detail.
func discoveryJoinDetails(gaps []DiscoveryGap) string {
	seen := make(map[string]bool, len(gaps))
	details := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		if gap.Detail == "" || seen[gap.Detail] {
			continue
		}
		seen[gap.Detail] = true
		details = append(details, gap.Detail)
	}
	sort.Strings(details)
	return strings.Join(details, "; ")
}

// branchHead returns the observed head of the default branch, matching the
// forge's default branch first and the repository's registered branch second.
func branchHead(branches []DiscoveryBranch, names ...string) string {
	for _, name := range names {
		if name == "" {
			continue
		}
		for _, branch := range branches {
			if branch.Name == name && branch.SHA != "" {
				return branch.SHA
			}
		}
	}
	return ""
}

// checkDetail describes one check without inventing a field it did not report.
func checkDetail(check DiscoveryCheck) string {
	parts := make([]string, 0, 4)
	if check.Conclusion != "" {
		parts = append(parts, "conclusion "+check.Conclusion)
	}
	if check.Status != "" {
		parts = append(parts, "status "+check.Status)
	}
	if check.AppID != 0 {
		parts = append(parts, fmt.Sprintf("app %d", check.AppID))
	}
	if check.URL != "" {
		parts = append(parts, check.URL)
	}
	return strings.Join(parts, "; ")
}

// failingChecks returns the checks discovery may fetch a log for, in a stable
// order so the exercised check never depends on map iteration.
func failingChecks(checks []DiscoveryCheck) []DiscoveryCheck {
	failing := make([]DiscoveryCheck, 0, len(checks))
	for _, check := range checks {
		if check.Conclusion == "failure" || check.Status == "failure" {
			failing = append(failing, check)
		}
	}
	sort.SliceStable(failing, func(i, j int) bool {
		if failing[i].Name != failing[j].Name {
			return failing[i].Name < failing[j].Name
		}
		return failing[i].JobID < failing[j].JobID
	})
	return failing
}

// recommend derives advisory suggestions from sections that were inspected
// successfully. A gapped section never produces one: an unreadable target is
// not evidence that the thing is missing.
func (a *discoveryAssembler) recommend() {
	if a.state(discoveryKeyInstructions) == DiscoveryCapabilitySupported &&
		a.hasAbsence(discoverySectionInstructions) {
		a.recs = append(a.recs, DiscoveryRecommendation{
			Origin:  discoveryRecommendedOrigin,
			Section: discoverySectionInstructions,
			Kind:    discoverySectionInstructions,
			Value:   "Add project instructions (AGENTS.md)",
			Reason:  "no project instructions were found in the checkout",
		})
	}
	if a.state(discoveryKeyEnvironment) == DiscoveryCapabilitySupported &&
		a.hasAbsence(discoverySectionEnvironment) {
		a.recs = append(a.recs, DiscoveryRecommendation{
			Origin:  discoveryRecommendedOrigin,
			Section: discoverySectionEnvironment,
			Kind:    discoverySectionEnvironment,
			Value:   "Define a development environment",
			Reason:  "no recognised environment definition was found",
		})
	}
}

// hasAbsence reports whether a section completed its search and found nothing.
func (a *discoveryAssembler) hasAbsence(section string) bool {
	for _, fact := range a.facts {
		if fact.Section == section && fact.Value == discoveryAbsenceValue {
			return true
		}
	}
	return false
}

// report stamps revisions, orders every slice deterministically, and derives
// the status from the assembled capabilities and gaps.
func (a *discoveryAssembler) report() *DiscoveryReport {
	report := &DiscoveryReport{
		RepoID:   a.repo.ID,
		Forge:    a.repo.Forge,
		Worktree: a.worktree,
	}

	report.Capabilities = make([]DiscoveryCapability, 0, len(discoveryCapabilityOrder))
	for _, key := range discoveryCapabilityKeys() {
		capability, ok := a.caps[key]
		if !ok {
			capability = DiscoveryCapability{
				Key:    key,
				State:  DiscoveryCapabilityUnsupported,
				Detail: "discovery did not probe this capability",
			}
		}
		report.Capabilities = append(report.Capabilities, capability)
	}

	facts := make([]DiscoveryFact, len(a.facts))
	copy(facts, a.facts)
	for i := range facts {
		if strings.HasPrefix(facts[i].Source, "forge:") {
			// Forge facts carry the default-branch head the probe observed, so
			// a stale report is detectable when that branch advances.
			facts[i].Revision = a.forgeHead
		}
	}
	sort.SliceStable(facts, func(i, j int) bool {
		left, right := facts[i], facts[j]
		switch {
		case left.Section != right.Section:
			return left.Section < right.Section
		case left.Kind != right.Kind:
			return left.Kind < right.Kind
		case left.Value != right.Value:
			return left.Value < right.Value
		case left.Source != right.Source:
			return left.Source < right.Source
		}
		return left.Detail < right.Detail
	})
	report.Facts = facts

	recs := make([]DiscoveryRecommendation, len(a.recs))
	copy(recs, a.recs)
	sort.SliceStable(recs, func(i, j int) bool {
		left, right := recs[i], recs[j]
		switch {
		case left.Section != right.Section:
			return left.Section < right.Section
		case left.Kind != right.Kind:
			return left.Kind < right.Kind
		}
		return left.Value < right.Value
	})
	report.Recommended = recs

	gaps := make([]DiscoveryGap, 0, len(a.gaps))
	for _, key := range discoveryCapabilityKeys() {
		if gap, ok := a.gaps[key]; ok {
			gaps = append(gaps, gap)
		}
	}
	report.Gaps = gaps

	// Top-level revision and branch prefer the linked checkout, then the
	// forge's default branch. A detached HEAD keeps its SHA and an empty
	// branch rather than borrowing the forge's branch name.
	if a.checkoutHead != "" {
		report.Revision = a.checkoutHead
		report.Branch = a.checkoutBranch
	} else {
		report.Revision = a.forgeHead
		report.Branch = a.forgeBase
	}

	switch {
	case a.unavail:
		report.Status = discoveryStatusUnavailable
		report.Error = a.outage
	case a.degraded(report.Capabilities, report.Gaps):
		report.Status = discoveryStatusDegraded
	default:
		report.Status = discoveryStatusOK
	}
	return report
}

// degraded reports whether any capability is not supported or any gap exists.
func (a *discoveryAssembler) degraded(caps []DiscoveryCapability, gaps []DiscoveryGap) bool {
	if len(gaps) > 0 {
		return true
	}
	for _, capability := range caps {
		if capability.State != DiscoveryCapabilitySupported {
			return true
		}
	}
	return false
}

// firstErrorLine bounds a subprocess error to its first line, so a git failure
// does not paste a multi-line message into a report detail.
func firstErrorLine(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	const limit = 300
	if len(msg) > limit {
		msg = msg[:limit] + "..."
	}
	return msg
}
