package contextpack

import (
	"strings"
	"testing"
	"time"
)

func fixedNow() time.Time { return time.Unix(1700000000, 0).UTC() }

func TestMergeRepoSectionsSeedsCardAndMap(t *testing.T) {
	pack := MergeRepoSections(Pack{}, "Project: x\nFiles: 2", "cmd/\n  main.go (Main)", 0, func() time.Time { return fixedNow() })
	if len(pack.Sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(pack.Sections))
	}
	if pack.Sections[0].Kind != SectionRepoCard || pack.Sections[1].Kind != SectionRepoMap {
		t.Fatalf("kinds = %s,%s; want repo_card,repo_map", pack.Sections[0].Kind, pack.Sections[1].Kind)
	}
	if pack.TokenUsage.MaxTokens != DefaultMaxTokens {
		t.Fatalf("maxTokens = %d, want default %d", pack.TokenUsage.MaxTokens, DefaultMaxTokens)
	}
}

func TestMergeRepoSectionsIsIdempotent(t *testing.T) {
	pack := MergeRepoSections(Pack{}, "card-v1", "map-v1", 0, nil)
	pack = MergeRepoSections(pack, "card-v2", "map-v2", 0, nil)
	if len(pack.Sections) != 2 {
		t.Fatalf("sections = %d, want 2 (no duplicates)", len(pack.Sections))
	}
	if pack.Sections[0].Content != "card-v2" || pack.Sections[1].Content != "map-v2" {
		t.Fatalf("re-seed did not replace contents: %+v", pack.Sections)
	}
}

func TestMergeRepoSectionsSkipsEmptyContent(t *testing.T) {
	pack := MergeRepoSections(Pack{}, "", "  \n", 0, nil)
	if len(pack.Sections) != 0 {
		t.Fatalf("sections = %d, want 0 for empty card+map", len(pack.Sections))
	}
	pack = MergeRepoSections(Pack{}, "card only", "", 0, nil)
	if len(pack.Sections) != 1 || pack.Sections[0].Kind != SectionRepoCard {
		t.Fatalf("sections = %+v, want only repo_card", pack.Sections)
	}
}

func TestMergeRepoSectionsStarvesMapBeforeCard(t *testing.T) {
	card := "Project: x"
	mapContent := strings.Repeat("dir/\n  file.go (Func)\n", 200) // large
	// Budget fits the card but not the map. The card (priority 10) is
	// allocated before the map and gets its full share.
	budget := EstimateTokens(card) * 2
	pack := MergeRepoSections(Pack{}, card, mapContent, budget, nil)
	if len(pack.Sections) == 0 || pack.Sections[0].Kind != SectionRepoCard {
		t.Fatalf("card should survive starvation, got %+v", pack.Sections)
	}
	for _, s := range pack.Sections {
		if s.Kind == SectionRepoMap && s.Content == mapContent {
			t.Fatal("map should have been truncated or dropped under tight budget")
		}
	}
}

func TestMergeRepoSectionsPreservesOtherSections(t *testing.T) {
	pack := MergeMemories(Pack{}, []MemoryNote{{Kind: "fact", Content: "remember me"}}, 0, nil)
	pack = MergeRepoSections(pack, "card", "map", 0, nil)
	if len(pack.Sections) != 3 {
		t.Fatalf("sections = %d, want 3", len(pack.Sections))
	}
	// Repo sections lead; memory follows.
	if pack.Sections[0].Kind != SectionRepoCard || pack.Sections[2].Kind != SectionMemory {
		t.Fatalf("order = %s,%s,%s; want repo_card,repo_map,memory",
			pack.Sections[0].Kind, pack.Sections[1].Kind, pack.Sections[2].Kind)
	}
}
