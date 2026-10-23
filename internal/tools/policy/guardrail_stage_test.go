package policy

import "testing"

// TestGuardrailBlocksWrappedRM pins S-06: destructive rm invocations must be
// blocked even when the top-level argv0 is a wrapper or the rm appears as an
// xargs payload. The canonical analyzer strips transparent wrappers and
// recurses into payloads, so these variants reach the destructive verdict.
func TestGuardrailBlocksWrappedRM(t *testing.T) {
	cases := []string{
		"env rm -fr /",
		"env rm -f -r /tmp/x",
		"env -i rm --recursive --force /tmp/x",
		"env FOO=bar rm -fr /tmp/x",
		"nice rm -fr /tmp/x",
		"nice -n 10 rm -f -r ~",
		"find . -type f | xargs rm -fr",
		"find . -type f | xargs -I {} rm -fr {}",
		"find . -type f | xargs -n 1 rm -f -r",
		"echo hi | rm -fr /tmp",
	}

	for _, cmd := range cases {
		a := analyze(t, cmd)
		if !hasEffect(a, ShellDestructive) {
			t.Errorf("%q: no destructive effect in %+v", cmd, a.Operations)
		}
	}
}

// TestGuardrailDoesNotFalsePositiveOnEchoRM verifies that a harmless echo of
// an rm-looking string is not blocked just because the tokens appear in the
// stage.
func TestGuardrailDoesNotFalsePositiveOnEchoRM(t *testing.T) {
	cases := []string{
		"echo rm -fr /",
		"echo rm -rf /tmp",
		"printf 'rm -fr /'",
	}

	for _, cmd := range cases {
		a := analyze(t, cmd)
		if hasEffect(a, ShellDestructive) {
			t.Errorf("%q: classified destructive: %+v", cmd, a.Operations)
		}
	}
}
