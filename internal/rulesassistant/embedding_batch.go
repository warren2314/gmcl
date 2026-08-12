package rulesassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	embeddingBatchSize        = 64
	embeddingDimension        = 1536
	maxEmbeddingResponseBytes = 16 << 20
)

// embedBatch embeds a bounded group in one upstream request. Results are
// returned in input order even when the API returns data out of order. A
// transient failure retries the entire batch, and no partial result is
// returned unless every index and vector has been validated.
func (s *Service) embedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if len(inputs) > embeddingBatchSize {
		return nil, fmt.Errorf("embedding batch has %d inputs; maximum is %d", len(inputs), embeddingBatchSize)
	}

	payload, err := json.Marshal(map[string]any{
		"model":           s.EmbedModel,
		"input":           inputs,
		"encoding_format": "float",
	})
	if err != nil {
		return nil, err
	}

	lastErr := errors.New("embedding request failed")
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			delay := time.Second << (attempt - 1)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/embeddings", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+s.APIKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.HTTPClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxEmbeddingResponseBytes+1))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if len(body) > maxEmbeddingResponseBytes {
			return nil, fmt.Errorf("embedding response exceeded %d bytes", maxEmbeddingResponseBytes)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("OpenAI embeddings status %d: %s", resp.StatusCode, truncate(string(body), 400))
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				continue
			}
			return nil, lastErr
		}

		var decoded struct {
			Data []struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("decode embedding response: %w", err)
		}
		if len(decoded.Data) != len(inputs) {
			return nil, fmt.Errorf("embedding response returned %d vectors for %d inputs", len(decoded.Data), len(inputs))
		}

		ordered := make([][]float32, len(inputs))
		seen := make([]bool, len(inputs))
		for _, item := range decoded.Data {
			if item.Index < 0 || item.Index >= len(inputs) {
				return nil, fmt.Errorf("embedding response index %d is outside batch of %d", item.Index, len(inputs))
			}
			if seen[item.Index] {
				return nil, fmt.Errorf("embedding response repeated index %d", item.Index)
			}
			if len(item.Embedding) != embeddingDimension {
				return nil, fmt.Errorf("embedding response index %d had dimension %d; expected %d", item.Index, len(item.Embedding), embeddingDimension)
			}
			seen[item.Index] = true
			ordered[item.Index] = item.Embedding
		}
		return ordered, nil
	}
	return nil, lastErr
}
