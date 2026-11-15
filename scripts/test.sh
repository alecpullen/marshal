#!/usr/bin/env bash
#
# scripts/test.sh — run the tests that are worth running, at the scope you ask
# for. Modes trade coverage for latency:
#
#   changed   (default) only the packages your diff can affect, uncached
#   quick     the whole suite with -short          (~2 min, skips the heavy scenarios)
#   full      the whole suite uncached             (~4 min warm, ~9 min cold)
#   budgets   the 12 wall-clock latency budgets    (needs an idle laptop)
#   ci        the three CI jobs, in order          (what a PR must pass)
#
# Everything after the mode that is not `--base` is passed through to `go test`:
#
#   scripts/test.sh                                # what did my working tree touch?
#   scripts/test.sh changed --base origin/main     # ... compared with a specific ref
#   scripts/test.sh quick ./internal/acp           # -short, one package
#   scripts/test.sh full -run TestSnapshot
#   scripts/test.sh budgets
#   scripts/test.sh ci --go-only                   # skip the web and usability jobs
#
# The base ref also reads BASE_REF. See docs/testing-ci-audit.md for the
# measurements behind these modes.

set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"

# tree-sitter (internal/repo/symbols.go) links a C library, so every build and
# test in this repository needs cgo.
export CGO_ENABLED=1

# The named wall-clock latency budgets. CI excludes them because a shared runner
# cannot honour an absolute millisecond threshold; `budgets` runs exactly them,
# on a machine that can.
readonly latency_tests='^Test(NoticeCallBudget|ControlCallBudget|AnswerCallBudget|ResponseBarrierBudget|SettingsCallBudget|NoteCallBudget|BrowserCallBudget|EndCallBudget|StackBusyFlipBudget|StackPatchApplyBudget|StackRoundTripBudget|AKeyPressOverThePipeBudget)$'
readonly budget_packages='./internal/acp ./internal/app/tui'

usage() {
	sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'
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
changed_files() {
	local base=${1:-}
	{
		if [ -n "$base" ]; then
			git diff --name-only "$base"...HEAD
		fi
		git diff --name-only
		git diff --cached --name-only
		git ls-files --others --exclude-standard
	} | sort -u
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

	if ! git rev-parse --verify --quiet "$base^{commit}" >/dev/null; then
		if [ -n "${BASE_REF:-}" ]; then
			die "base ref '$base' is not a commit"
		fi
		base=''
	fi

	list_packages >"$scratch/packages"
	changed_files "$base" >"$scratch/files"
	packages_for_files "$scratch/packages" "$scratch/files" >"$scratch/direct"

	if [ ! -s "$scratch/direct" ]; then
		printf 'test.sh: no Go package changed against %s\n' "${base:-the working tree}" >&2
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
[ "$#" -gt 0 ] && shift

case "$mode" in
changed | quick | full | budgets | ci) ;;
-h | --help | help) usage; exit 0 ;;
*) die "unknown mode '$mode' (changed, quick, full, budgets, ci)" ;;
esac

# Pull our own flags out; everything else belongs to `go test`. A bare `--` is
# accepted and dropped so `full -- -run X` cannot be mistaken for a go flag.
go_only=false
base=${BASE_REF:-origin/main}
args=()
while [ "$#" -gt 0 ]; do
	case "$1" in
	--)
		shift
		;;
	--go-only) go_only=true; shift ;;
	--base | --base=*)
		if [ "$1" = '--base' ]; then
			[ "$#" -ge 2 ] || die '--base needs a ref'
			base=$2
			shift 2
		else
			base=${1#--base=}
			shift
		fi
		;;
	*)
		args+=("$1")
		shift
		;;
	esac
done
if [ "${#args[@]}" -gt 0 ]; then set -- "${args[@]}"; else set --; fi

case "$mode" in
changed)
	run_changed "$@"
	;;
quick)
	# -short keeps every state and theme in the TUI sweep but cuts the size
	# matrix, and skips the heavy Git, crash-recovery and process-integration
	# scenarios listed in docs/testing-ci-audit.md.
	go test -short -timeout 20m "$@" ./...
	;;
full)
	go test -count=1 -timeout 20m "$@" ./...
	;;
budgets)
	# Wall-clock thresholds: run these with no other work on the machine.
	# shellcheck disable=SC2086
	go test -count=1 -p=1 -timeout 20m -run "$latency_tests" "$@" $budget_packages
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

	if command -v npm >/dev/null 2>&1; then
		(
			cd web/ui
			npm ci
			npm test
			npm run build
		)
		git diff --exit-code -- web/bridge/static
		if [ -n "$(git ls-files --others --exclude-standard -- web/bridge/static)" ]; then
			die 'web/bridge/static has build output that was never committed'
		fi
	else
		printf 'test.sh: npm not found, skipping the web UI job\n' >&2
	fi
	;;
esac
