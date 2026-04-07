package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/charliewilco/elephas"
)

func TestExtractBuildsOpenAIRequestAndParsesCandidates(t *testing.T) {
	var sawAuth string
	var sawRequest chatCompletionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &sawRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		response := chatCompletionResponse{
			Choices: []struct {
				Message chatMessage `json:"message"`
			}{
				{Message: chatMessage{Role: "assistant", Content: `[{"content":"Prefers dark mode","category":"preference","confidence":0.91,"subject":{"name":"Charlie","type":"person"},"related_entities":[{"name":"Elephas","type":"agent"}],"relationship_type":"uses"}]`}},
			},
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	extractor := New(Config{
		Endpoint:      server.URL,
		APIKey:        "secret",
		DefaultModel:  "gpt-4o-mini",
		Timeout:       time.Second,
		MaxCandidates: 10,
	})

	candidates, err := extractor.Extract(context.Background(), elephas.ExtractRequest{
		RawText:           "Charlie prefers dark mode and uses Elephas.",
		SubjectEntityName: "Charlie",
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	if sawAuth != "Bearer secret" {
		t.Fatalf("expected bearer token, got %q", sawAuth)
	}
	if sawRequest.Model != "gpt-4o-mini" {
		t.Fatalf("expected default model, got %s", sawRequest.Model)
	}
	if len(sawRequest.Messages) != 2 {
		t.Fatalf("expected two messages, got %d", len(sawRequest.Messages))
	}
	if len(candidates) != 1 || candidates[0].Subject == nil || candidates[0].Subject.Name != "Charlie" {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
}

func TestExtractUsesSystemPromptOverrideAndModelOverride(t *testing.T) {
	var sawRequest chatCompletionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &sawRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(chatCompletionResponse{
			Choices: []struct {
				Message chatMessage `json:"message"`
			}{
				{Message: chatMessage{Role: "assistant", Content: `[]`}},
			},
		})
	}))
	defer server.Close()

	extractor := New(Config{
		Endpoint:      server.URL,
		APIKey:        "secret",
		DefaultModel:  "gpt-4o-mini",
		Timeout:       time.Second,
		MaxCandidates: 10,
	})

	if _, err := extractor.Extract(context.Background(), elephas.ExtractRequest{
		RawText:              "Charlie likes tea.",
		Model:                "gpt-4.1-mini",
		SystemPromptOverride: "custom prompt",
	}); err != nil {
		t.Fatalf("extract: %v", err)
	}

	if sawRequest.Model != "gpt-4.1-mini" {
		t.Fatalf("expected model override, got %s", sawRequest.Model)
	}
	if got := sawRequest.Messages[0].Content; got != "custom prompt" {
		t.Fatalf("expected prompt override, got %q", got)
	}
}

func TestExtractReportsErrorPaths(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want string
	}{
		{
			name: "non-2xx",
			code: http.StatusBadGateway,
			body: `bad gateway`,
			want: "extractor returned non-success status",
		},
		{
			name: "malformed json",
			code: http.StatusOK,
			body: `{"choices":[{"message":{"content":"not json"}}]}`,
			want: "extractor returned malformed JSON",
		},
		{
			name: "too many candidates",
			code: http.StatusOK,
			body: `{"choices":[{"message":{"content":"[{\"content\":\"one\",\"category\":\"fact\",\"confidence\":1},{\"content\":\"two\",\"category\":\"fact\",\"confidence\":1}]"}}]}`,
			want: "extractor returned too many candidates",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.code)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			extractor := New(Config{
				Endpoint:      server.URL,
				APIKey:        "secret",
				DefaultModel:  "gpt-4o-mini",
				Timeout:       time.Second,
				MaxCandidates: 1,
			})

			_, err := extractor.Extract(context.Background(), elephas.ExtractRequest{RawText: "Charlie"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q in error, got %v", tt.want, err)
			}
		})
	}
}

func TestValidateConfig(t *testing.T) {
	if err := ValidateConfig(Config{}); err == nil {
		t.Fatalf("expected missing config to fail")
	}
	if err := ValidateConfig(Config{Endpoint: "https://example.com", APIKey: "secret", DefaultModel: "gpt-4o-mini"}); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}
