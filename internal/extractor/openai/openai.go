package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/charliewilco/elephas"
)

type Config struct {
	Endpoint      string
	APIKey        string
	DefaultModel  string
	Timeout       time.Duration
	MaxCandidates int
}

type Extractor struct {
	config Config
	client *http.Client
}

func New(config Config) *Extractor {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Extractor{
		config: config,
		client: &http.Client{Timeout: timeout},
	}
}

func (e *Extractor) Extract(ctx context.Context, request elephas.ExtractRequest) ([]elephas.CandidateMemory, error) {
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = e.config.DefaultModel
	}

	payload := chatCompletionRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: defaultSystemPrompt(request.SystemPromptOverride)},
			{Role: "user", Content: request.RawText},
		},
		Temperature: 0,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, elephas.WrapError(elephas.ErrorCodeExtractionFailed, "marshal extractor request", err, nil)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, e.config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, elephas.WrapError(elephas.ErrorCodeExtractionFailed, "build extractor request", err, nil)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+e.config.APIKey)
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := e.client.Do(httpRequest)
	if err != nil {
		return nil, elephas.WrapError(elephas.ErrorCodeExtractorUnavailable, "extractor request failed", err, nil)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, elephas.WrapError(elephas.ErrorCodeExtractionFailed, "read extractor response", err, nil)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, elephas.NewError(elephas.ErrorCodeExtractorUnavailable, "extractor returned non-success status", map[string]any{
			"status": response.StatusCode,
			"body":   string(responseBody),
		})
	}

	var completion chatCompletionResponse
	if err := json.Unmarshal(responseBody, &completion); err != nil {
		return nil, elephas.WrapError(elephas.ErrorCodeExtractionFailed, "decode extractor response", err, nil)
	}
	if len(completion.Choices) == 0 {
		return nil, elephas.NewError(elephas.ErrorCodeExtractionFailed, "extractor returned no choices", nil)
	}

	content := completion.Choices[0].Message.Content
	var candidates []elephas.CandidateMemory
	if err := json.Unmarshal([]byte(content), &candidates); err != nil {
		return nil, elephas.WrapError(elephas.ErrorCodeExtractionFailed, "extractor returned malformed JSON", err, map[string]any{
			"content": content,
		})
	}

	if e.config.MaxCandidates > 0 && len(candidates) > e.config.MaxCandidates {
		return nil, elephas.NewError(elephas.ErrorCodeExtractionFailed, "extractor returned too many candidates", map[string]any{
			"count": len(candidates),
			"max":   e.config.MaxCandidates,
		})
	}

	for i := range candidates {
		if candidates[i].Subject != nil && strings.TrimSpace(candidates[i].Subject.Name) == "" {
			candidates[i].Subject = nil
		}
		if request.SubjectEntityName != "" && candidates[i].Subject == nil {
			candidates[i].Subject = &elephas.CandidateEntity{
				Name: request.SubjectEntityName,
				Type: elephas.EntityTypePerson,
			}
		}
	}

	return candidates, nil
}

func defaultSystemPrompt(override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}

	return strings.TrimSpace(`
Return only a JSON array of memory candidates. Do not include markdown or commentary.
Each item must follow this schema:
{
  "content": "atomic fact",
  "category": "preference|fact|relationship|event|instruction",
  "confidence": 0.0,
  "subject": {"name": "entity name", "type": "person|organization|place|concept|object|agent"},
  "related_entities": [{"name": "entity name", "type": "person|organization|place|concept|object|agent"}],
  "relationship_type": "snake_case relationship label"
}
Rules:
- Emit one item per discrete fact.
- Use "relationship" category only when the fact implies an explicit edge between the subject and one or more related entities.
- Omit relationship_type unless the fact expresses a relationship.
- Prefer specific, atomic facts over summaries.
- Confidence must be between 0 and 1.
`)
}

type chatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

var _ elephas.Extractor = (*Extractor)(nil)

func ValidateConfig(config Config) error {
	if strings.TrimSpace(config.Endpoint) == "" {
		return errors.New("endpoint is required")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return errors.New("api key is required")
	}
	if strings.TrimSpace(config.DefaultModel) == "" {
		return fmt.Errorf("default model is required")
	}
	return nil
}
