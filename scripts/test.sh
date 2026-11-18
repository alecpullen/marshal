#!/usr/bin/env bash
#
# scripts/test.sh — run the tests that are worth running, at the scope you ask
# for. Modes trade coverage for latency:
#
#   changed   (default) only the packages your diff can affect, uncached
#   quick     the whole suite with -short          (~2 min warm cache, heavy scenarios skipped)
#   full      the whole suite uncached             (2m50 warm, ~9m cold)
#   budgets   the 12 wall-clock latency budgets    (needs an idle laptop)
#   ci        the three CI jobs, in order          (what a PR must pass)
#
# Everything after the mode that is not `--base`/`--go-only` is passed through to
# `go test`. Name a package and the mode covers only that one; name none and
# `quick`/`full` cover ./... while `budgets` covers its own two packages:
#
#   scripts/test.sh                                # what did my working tree touch?
#   scripts/test.sh changed --base origin/main     # ... compared with a specific ref
#   scripts/test.sh quick ./internal/acp           # -short, that package only
#   scripts/test.sh full -run TestSnapshot         # ./..., one test name
#   scripts/test.sh budgets
#   scripts/test.sh ci --go-only                   # skip the web and usability jobs
#
# `quick` leaves Go's test cache on, so a repeat run is only as slow as what
# changed; pass -count=1 to force the whole suite.
#
# The base ref also reads BASE_REF and must resolve: an explicit --base that does
# not is an error, not an empty green run. See docs/testing-ci-audit.md for the
# measurements behind these modes.

set -euo pipefail

# -P: the physical path, so it matches the absolute Dir `go list` reports even
# from a symlinked checkout.
repo_root=$(cd -P "$(dirname "$0")/.." && pwd -P)
cd "$repo_root"

# tree-sitter (internal/repo/symbols.go) links a C library, so every build and
# test in this repository needs cgo.
export CGO_ENABLED=1

# The named wall-clock latency budgets. CI excludes them because a shared runner
# cannot honour an absolute millisecond threshold; `budgets` runs exactly them,
# on a machine that can.
readonly latency_tests='^Test(NoticeCallBudget|ControlCallBudget|AnswerCallBudget|ResponseBarrierBudget|SettingsCallBudget|NoteCallBudget|BrowserCallBudget|EndCallBudget|StackBusyFlipBudget|StackPatchApplyBudget|StackRoundTripBudget|AKeyPressOverThePipeBudget)$'
readonly budget_packages='./internal/acp ./internal/app/tui'

# The header comment is the help text: print it up to the first line that is not
# a comment, so the two cannot drift apart.
usage() {
	awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"
}

die() {
	printf 'test.sh: %s\n' "$1" >&2
	exit 2
}

# ---------------------------------------------------------------------------
# changed: the packages the working tree can affect, plus everything that
# imports them.
# ---------------------------------------------------------------------------

# Package directories, one "import/path|/absolute/dir" line each.
list_packages() {
	go list -f '{{.ImportPath}}|{{.Dir}}' ./...
}

# The files this checkout has touched: committed since the base ref, staged,
# unstaged and untracked. Duplicates are removed.
#
# Renames matter in both directions: `--name-only` prints only the new path, so a
# file moved out of a package would leave that package untested — and it is the
# package that just lost a symbol whose tests break. `--name-status -M` names
# both sides of an R/C entry; every other status names one path in $2.
changed_files() {
	local base=${1:-}
	{
		if [ -n "$base" ]; then
			git diff --name-status -M "$base"...HEAD
		fi
		git diff --name-status -M
		git diff --cached --name-status -M
		git ls-files --others --exclude-standard
	} | awk -F'\t' '
		$1 ~ /^[RC][0-9]*$/ { print $2; print $3; next }
		NF >= 2 { print $2; next }
		NF == 1 { print $1 }
	' | sort -u
}

# The package owning each changed file. A file below a package directory
# (`testdata/`, `static/`, `builtin/`) belongs to its nearest package ancestor:
# `//go:embed` and testdata are both inputs to that package's test result. A
# file under no package (docs, scripts) maps to nothing.
packages_for_files() {
	awk -F'|' -v root="$repo_root/" '
		NR == FNR { pkgdir[$2] = $1; next }
		{
			dir = root $0
			sub(/\/[^\/]*$/, "", dir)
			while (dir != "" && !(dir in pkgdir)) {
				if (!sub(/\/[^\/]*$/, "", dir)) break
			}
			if (dir != "" && (dir in pkgdir)) print pkgdir[dir]
		}
	' "$1" "$2" | sort -u
}

# Transitive dependents: a package is affected when it is changed itself or when
# one of its dependencies is. `go list -test -deps` records each package's
# transitive closure and adds its test variants, so a package whose *test*
# imports something you changed is caught too. The synthetic variants
# (`x.test`, `x [x.test]`, `x_test [x.test]`) are attributed to the package they
# test rather than tested as targets of their own.
affected_packages() {
	awk -F'|' -v knownfile="$1" -v hitfile="$2" '
		FILENAME == knownfile { known[$1] = 1; next }
		FILENAME == hitfile { hit[$1] = 1; next }
		{
			name = $1
			sub(/ .*/, "", name)
			sub(/\.test$/, "", name)
			if (!(name in known)) {
				# An external test package (x_test) belongs to x.
				base = name
				sub(/_test$/, "", base)
				if (base == name || !(base in known)) next
				name = base
			}
			if (name in hit) { mark[name] = 1; next }
			n = split($2, deps, " ")
			for (i = 1; i <= n; i++) {
				if (deps[i] in hit) { mark[name] = 1; break }
			}
		}
		END { for (p in mark) print p }
	' "$1" "$2" "$3" | sort -u
}

run_changed() {
	local scratch
	scratch=$(mktemp -d)
	# The path is baked into the trap: a function-local variable would be gone by
	# the time the EXIT trap fires.
	trap "rm -rf '$scratch'" EXIT

	# A base the caller named must resolve. Degrading to "nothing changed" would
	# report a green run that executed no tests at all, which is the one failure a
	# test runner must not have; only the implicit origin/main default may fall
	# back, and it says so.
	if ! git rev-parse --verify --quiet "$base^{commit}" >/dev/null; then
		if $base_explicit; then
			die "base ref '$base' is not a commit"
		fi
		printf 'test.sh: base ref %s not found; checking the working tree only\n' "$base" >&2
		base=''
	fi

	list_packages >"$scratch/packages"
	changed_files "$base" >"$scratch/files"
	packages_for_files "$scratch/packages" "$scratch/files" >"$scratch/direct"

	if [ ! -s "$scratch/files" ]; then
		printf 'test.sh: nothing changed against %s\n' "${base:-the working tree}" >&2
		return 0
	fi
	if [ ! -s "$scratch/direct" ]; then
		printf 'test.sh: %s file(s) changed against %s, none of them under a Go package\n' \
			"$(wc -l <"$scratch/files" | tr -d ' ')" "${base:-the working tree}" >&2
		printf 'test.sh: a Go test run cannot cover those; `scripts/test.sh ci` can\n' >&2
		return 0
	fi

	go list -test -deps -f '{{.ImportPath}}|{{join .Deps " "}}' ./... >"$scratch/deps"
	affected_packages "$scratch/packages" "$scratch/direct" "$scratch/deps" >"$scratch/affected"

	printf 'test.sh: %s changed package(s) against %s; testing %s affected\n' \
		"$(wc -l <"$scratch/direct" | tr -d ' ')" "${base:-the working tree}" \
		"$(wc -l <"$scratch/affected" | tr -d ' ')" >&2
	sed 's/^/  /' "$scratch/affected" >&2

	# -count=1 because the point of this mode is a real result for the cone, not
	# a cached report for packages whose inputs changed. The cone is what keeps
	# that honest run cheap.
	# shellcheck disable=SC2046
	go test -count=1 -timeout 20m "$@" $(cat "$scratch/affected")
}

# ---------------------------------------------------------------------------
# Modes
# ---------------------------------------------------------------------------

mode=${1:-changed}
if [ "$#" -gt 0 ]; then shift; fi

case "$mode" in
changed | quick | full | budgets | ci) ;;
-h | --help | help) usage; exit 0 ;;
*) die "unknown mode '$mode' (changed, quick, full, budgets, ci)" ;;
esac

# Pull our own flags out; everything else belongs to `go test`. A bare `--` is
# accepted and dropped so `full -- -run X` cannot be mistaken for a go flag.
go_only=false
base=${BASE_REF:-origin/main}
# A base the caller named must resolve, so that a typo cannot turn into a green
# run that tested nothing. The implicit origin/main default is allowed to fall
# back to the working tree.
base_explicit=false
if [ -n "${BASE_REF:-}" ]; then base_explicit=true; fi
args=()
while [ "$#" -gt 0 ]; do
	case "$1" in
	--)
		shift
		;;
	--go-only) go_only=true; shift ;;
	--base | --base=*)
		if [ "$1" = '--base' ]; then
			case "${2-}" in
			'' | -*) die '--base needs a ref' ;;
			esac
			base=$2
			shift 2
		else
			base=${1#--base=}
			[ -n "$base" ] || die '--base needs a ref'
			shift
		fi
		base_explicit=true
		;;
	*)
		args+=("$1")
		shift
		;;
	esac
done
if [ "${#args[@]}" -gt 0 ]; then set -- "${args[@]}"; else set --; fi

# Did the caller name packages, or only pass flags? Appending ./... to a call that
# already named one would silently widen it to the whole suite — the opposite of
# what `quick ./internal/acp` asks for. A flag's value (`-run TestStack`) is a
# bare word, so only path-shaped words count as packages.
names_packages() {
	local arg
	for arg in "$@"; do
		case "$arg" in
		./* | ../* | /* | .) return 0 ;;
		esac
	done
	return 1
}

case "$mode" in
changed)
	run_changed "$@"
	;;
quick)
	# -short keeps every state and theme in the TUI sweep but cuts the size
	# matrix, and skips the heavy Git, crash-recovery and process-integration
	# scenarios listed in docs/testing-ci-audit.md.
	if names_packages "$@"; then
		go test -short -timeout 20m "$@"
	else
		go test -short -timeout 20m "$@" ./...
	fi
	;;
full)
	if names_packages "$@"; then
		go test -count=1 -timeout 20m "$@"
	else
		go test -count=1 -timeout 20m "$@" ./...
	fi
	;;
budgets)
	# Wall-clock thresholds: run these with no other work on the machine.
	if names_packages "$@"; then
		go test -count=1 -p=1 -timeout 20m -run "$latency_tests" "$@"
	else
		# shellcheck disable=SC2086
		go test -count=1 -p=1 -timeout 20m -run "$latency_tests" "$@" $budget_packages
	fi
	;;
ci)
	# The Go job, then vet; the remaining two jobs only with the full run.
	go test -p=1 -count=1 -timeout 20m -skip "$latency_tests" "$@" ./...
	go vet ./...
	if $go_only; then
		exit 0
	fi

	go build ./cmd/marshal
	USABILITY_REQUIRE_BINARY=1 go test ./test/usability/... -run TestScripted -v -count=1 -timeout 5m
	;;
esac
