package rulesassistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type testEmbeddingDatum struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

func testEmbeddingVector(value float32) []float32 {
	vector := make([]float32, embeddingDimension)
	for i := range vector {
		vector[i] = value
	}
	return vector
}

func testEmbeddingResponse(t *testing.T, data []testEmbeddingDatum) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestEmbedBatchUsesResponseIndices(t *testing.T) {
	service := &Service{
		APIKey: "test-key", EmbedModel: "test-embedding-model",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Fatalf("authorization=%q", got)
			}
			var request struct {
				Model          string   `json:"model"`
				Input          []string `json:"input"`
				EncodingFormat string   `json:"encoding_format"`
			}
			if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Model != "test-embedding-model" || request.EncodingFormat != "float" {
				t.Fatalf("request=%+v", request)
			}
			if len(request.Input) != 2 || request.Input[0] != "first" || request.Input[1] != "second" {
				t.Fatalf("inputs=%q", request.Input)
			}
			body := testEmbeddingResponse(t, []testEmbeddingDatum{
				{Index: 1, Embedding: testEmbeddingVector(22)},
				{Index: 0, Embedding: testEmbeddingVector(11)},
			})
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})},
	}

	got, err := service.embedBatch(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("embedBatch returned error: %v", err)
	}
	if len(got) != 2 || got[0][0] != 11 || got[1][0] != 22 {
		t.Fatalf("vectors were not restored to input order: %v %v", got[0][0], got[1][0])
	}
}

func TestEmbedBatchRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name        string
		data        []testEmbeddingDatum
		errContains string
	}{
		{
			name:        "missing vector",
			data:        []testEmbeddingDatum{{Index: 0, Embedding: testEmbeddingVector(1)}},
			errContains: "1 vectors for 2 inputs",
		},
		{
			name: "duplicate index",
			data: []testEmbeddingDatum{
				{Index: 0, Embedding: testEmbeddingVector(1)},
				{Index: 0, Embedding: testEmbeddingVector(2)},
			},
			errContains: "repeated index 0",
		},
		{
			name: "out of range index",
			data: []testEmbeddingDatum{
				{Index: 0, Embedding: testEmbeddingVector(1)},
				{Index: 2, Embedding: testEmbeddingVector(2)},
			},
			errContains: "index 2 is outside batch of 2",
		},
		{
			name: "wrong dimension",
			data: []testEmbeddingDatum{
				{Index: 0, Embedding: testEmbeddingVector(1)},
				{Index: 1, Embedding: make([]float32, embeddingDimension-1)},
			},
			errContains: "dimension 1535; expected 1536",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			body := testEmbeddingResponse(t, tt.data)
			service := &Service{
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					attempts++
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})},
			}
			got, err := service.embedBatch(context.Background(), []string{"first", "second"})
			if err == nil || !strings.Contains(err.Error(), tt.errContains) {
				t.Fatalf("vectors=%v error=%v; want error containing %q", got, err, tt.errContains)
			}
			if attempts != 1 || got != nil {
				t.Fatalf("attempts=%d vectors=%v", attempts, got)
			}
		})
	}
}

func TestEmbedBatchRetriesWholeBatch(t *testing.T) {
	attempts := 0
	var attemptedInputs [][]string
	service := &Service{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			var request struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			attemptedInputs = append(attemptedInputs, append([]string(nil), request.Input...))
			if attempts == 1 {
				return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("temporary")), Header: make(http.Header)}, nil
			}
			body := testEmbeddingResponse(t, []testEmbeddingDatum{
				{Index: 0, Embedding: testEmbeddingVector(1)},
				{Index: 1, Embedding: testEmbeddingVector(2)},
			})
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})},
	}

	got, err := service.embedBatch(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("embedBatch returned error: %v", err)
	}
	if attempts != 2 || len(got) != 2 {
		t.Fatalf("attempts=%d vectors=%d", attempts, len(got))
	}
	for i, inputs := range attemptedInputs {
		if len(inputs) != 2 || inputs[0] != "first" || inputs[1] != "second" {
			t.Fatalf("attempt %d inputs=%q", i+1, inputs)
		}
	}
}
