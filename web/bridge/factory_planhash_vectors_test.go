package bridge

import "testing"

// TestFactoryPlanHashVectors pins FactoryPlanHash to literal digests so the
// Studio's TypeScript mirror (web/ui/src/lib/factory.ts, factoryPlanHash) can
// assert the same bytes. The algorithm is SHA-256 over the plan and the
// specification, each prefixed with its own 8-byte big-endian byte length: a
// language that hashes characters rather than UTF-8 bytes, or that uses a
// little-endian length, produces a different digest and this test plus its
// TypeScript twin is what catches it.
func TestFactoryPlanHashVectors(t *testing.T) {
	vectors := []struct {
		name string
		plan string
		spec string
		want string
	}{
		{
			// Two empty strings: the digest of nothing but the two zero
			// length prefixes, which is the whole framing in one vector.
			name: "empty plan and specification",
			plan: "",
			spec: "",
			want: "374708fff7719dd5979ec875d56cd2286f6d3cf7ec317a3b25632aab28ec37bb",
		},
		{
			// "é" is two UTF-8 bytes, so a length taken in characters (and a
			// UTF-16 encoder) both diverge from this digest.
			name: "whitespace and a multi-byte character are part of the hash",
			plan: "plan v2\n",
			spec: "spéc v2",
			want: "a7be07bf8aae8d9356ab7ee4bf6b3d42660fd3ef58e27a870ed64bb95a0b3207",
		},
	}
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			if got := FactoryPlanHash(v.plan, v.spec); got != v.want {
				t.Fatalf("FactoryPlanHash(%q, %q) = %s, want %s", v.plan, v.spec, got, v.want)
			}
		})
	}
}
