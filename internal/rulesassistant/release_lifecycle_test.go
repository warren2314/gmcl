package rulesassistant

import "testing"

func TestRuleDocumentHashIncludesParserVersion(t *testing.T) {
	const text = "unchanged published rule text"
	want := hashText("v2\x00" + text)
	if got := ruleDocumentHash(text); got != want {
		t.Fatalf("ruleDocumentHash() = %q, want %q", got, want)
	}
	if got := ruleDocumentHash(text); got == hashText(text) {
		t.Fatal("versioned document hash unexpectedly matches the legacy text-only hash")
	}
}

func TestCandidateReleaseShapeIsCanonicalAndCountsChunks(t *testing.T) {
	docs := []parsedDocument{
		{URL: "https://example.test/z", Hash: "z-hash", Chunks: []parsedChunk{{}, {}}},
		{URL: "https://example.test/a", Hash: "a-hash", Chunks: []parsedChunk{{}}},
	}
	got := candidateReleaseShape(docs)
	want := []releaseDocumentShape{
		{CanonicalURL: "https://example.test/a", ContentHash: "a-hash", ChunkCount: 1},
		{CanonicalURL: "https://example.test/z", ContentHash: "z-hash", ChunkCount: 2},
	}
	if !equalReleaseShape(got, want) {
		t.Fatalf("candidateReleaseShape() = %#v, want %#v", got, want)
	}
	if got := parsedChunkCount(docs); got != 3 {
		t.Fatalf("parsedChunkCount() = %d, want 3", got)
	}
}

func TestEqualReleaseShapeRequiresExactDocumentHashAndChunkCount(t *testing.T) {
	base := []releaseDocumentShape{{CanonicalURL: "https://example.test/a", ContentHash: "hash", ChunkCount: 2}}
	tests := []struct {
		name   string
		active []releaseDocumentShape
		want   bool
	}{
		{name: "exact", active: []releaseDocumentShape{{CanonicalURL: "https://example.test/a", ContentHash: "hash", ChunkCount: 2}}, want: true},
		{name: "different url", active: []releaseDocumentShape{{CanonicalURL: "https://example.test/b", ContentHash: "hash", ChunkCount: 2}}},
		{name: "different hash", active: []releaseDocumentShape{{CanonicalURL: "https://example.test/a", ContentHash: "other", ChunkCount: 2}}},
		{name: "different chunks", active: []releaseDocumentShape{{CanonicalURL: "https://example.test/a", ContentHash: "hash", ChunkCount: 3}}},
		{name: "missing document", active: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := equalReleaseShape(base, tt.active); got != tt.want {
				t.Fatalf("equalReleaseShape() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMissingActiveSourceURLs(t *testing.T) {
	active := []releaseDocumentShape{
		{CanonicalURL: "https://example.test/rules-b"},
		{CanonicalURL: "https://example.test/rules-a"},
	}
	t.Run("exact candidate", func(t *testing.T) {
		candidate := []releaseDocumentShape{
			{CanonicalURL: "https://example.test/rules-a"},
			{CanonicalURL: "https://example.test/rules-b"},
		}
		if got := missingActiveSourceURLs(active, candidate); len(got) != 0 {
			t.Fatalf("missingActiveSourceURLs() = %v, want none", got)
		}
	})
	t.Run("addition is allowed but removal is reported", func(t *testing.T) {
		candidate := []releaseDocumentShape{
			{CanonicalURL: "https://example.test/rules-c"},
			{CanonicalURL: "https://example.test/rules-b"},
		}
		got := missingActiveSourceURLs(active, candidate)
		if len(got) != 1 || got[0] != "https://example.test/rules-a" {
			t.Fatalf("missingActiveSourceURLs() = %v, want rules-a", got)
		}
	})
}

func TestReleaseCanBeActivatedOnlyForPublishedReleases(t *testing.T) {
	for _, status := range []string{"active", "archived"} {
		if !releaseCanBeActivated(status) {
			t.Fatalf("releaseCanBeActivated(%q) = false, want true", status)
		}
	}
	for _, status := range []string{"building", "failed", "unchanged", "future_status"} {
		if releaseCanBeActivated(status) {
			t.Fatalf("releaseCanBeActivated(%q) = true, want false", status)
		}
	}
}
