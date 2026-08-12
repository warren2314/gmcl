package rulesassistant

import (
	"context"
	"sort"
)

// ruleDocumentHashParserVersion is part of each document hash. Bump it when
// parser behaviour changes so one fresh release is built even if the visible
// page text is unchanged.
const ruleDocumentHashParserVersion = "v2"

type releaseDocumentShape struct {
	CanonicalURL string
	ContentHash  string
	ChunkCount   int
}

func ruleDocumentHash(text string) string {
	return hashText(ruleDocumentHashParserVersion + "\x00" + text)
}

func parsedChunkCount(docs []parsedDocument) int {
	total := 0
	for _, doc := range docs {
		total += len(doc.Chunks)
	}
	return total
}

func candidateReleaseShape(docs []parsedDocument) []releaseDocumentShape {
	shape := make([]releaseDocumentShape, 0, len(docs))
	for _, doc := range docs {
		shape = append(shape, releaseDocumentShape{
			CanonicalURL: doc.URL,
			ContentHash:  doc.Hash,
			ChunkCount:   len(doc.Chunks),
		})
	}
	sort.Slice(shape, func(i, j int) bool { return shape[i].CanonicalURL < shape[j].CanonicalURL })
	return shape
}

func equalReleaseShape(candidate, active []releaseDocumentShape) bool {
	if len(candidate) != len(active) {
		return false
	}
	for i := range candidate {
		if candidate[i] != active[i] {
			return false
		}
	}
	return true
}

func missingActiveSourceURLs(active, candidate []releaseDocumentShape) []string {
	candidateURLs := make(map[string]struct{}, len(candidate))
	for _, document := range candidate {
		candidateURLs[document.CanonicalURL] = struct{}{}
	}
	missing := make([]string, 0)
	for _, document := range active {
		if _, ok := candidateURLs[document.CanonicalURL]; !ok {
			missing = append(missing, document.CanonicalURL)
		}
	}
	sort.Strings(missing)
	return missing
}

func (s *Service) activeReleaseShape(ctx context.Context) ([]releaseDocumentShape, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT d.canonical_url, d.content_hash, COUNT(c.id)
		FROM rule_documents d
		JOIN rule_releases r ON r.id=d.release_id
		LEFT JOIN rule_chunks c ON c.document_id=d.id
		WHERE r.status='active'
		GROUP BY d.canonical_url, d.content_hash
		ORDER BY d.canonical_url`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var shape []releaseDocumentShape
	for rows.Next() {
		var item releaseDocumentShape
		var chunkCount int64
		if err := rows.Scan(&item.CanonicalURL, &item.ContentHash, &chunkCount); err != nil {
			return nil, err
		}
		item.ChunkCount = int(chunkCount)
		shape = append(shape, item)
	}
	return shape, rows.Err()
}

func releaseCanBeActivated(status string) bool {
	return status == "active" || status == "archived"
}
