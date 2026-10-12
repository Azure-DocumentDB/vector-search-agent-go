package agents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/models"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/prompts"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/safety"
	"github.com/openai/openai-go/v3"
)

type SearchStore interface {
	VectorSearch(context.Context, []float32, int) ([]models.HotelSearchResult, error)
}
type Embedder interface {
	GenerateEmbedding(context.Context, string) ([]float32, error)
}
type VectorSearchTool struct {
	embedder Embedder
	store    SearchStore
}

func NewVectorSearchTool(embedder Embedder, store SearchStore) *VectorSearchTool {
	return &VectorSearchTool{embedder, store}
}
func (t *VectorSearchTool) Execute(ctx context.Context, query string, k int) (string, error) {
	if err := safety.Query(query, k); err != nil {
		return "", err
	}
	vector, err := t.embedder.GenerateEmbedding(ctx, query)
	if err != nil {
		return "", err
	}
	results, err := t.store.VectorSearch(ctx, vector, k)
	if err != nil {
		return "", err
	}
	if len(results) == 0 || len(results) > k {
		return "", errors.New("search returned invalid result count")
	}
	for i := range results {
		results[i].Hotel.DescriptionVector = nil
	}
	data, err := json.Marshal(results)
	if err != nil || len(data) > 65536 {
		return "", errors.New("search context invalid or too large")
	}
	return string(data), nil
}
func (t *VectorSearchTool) GetToolDefinition() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionToolUnionParam{OfFunction: &openai.ChatCompletionFunctionToolParam{Function: openai.FunctionDefinitionParam{Name: prompts.ToolName, Description: openai.String(prompts.ToolDescription), Strict: openai.Bool(true), Parameters: map[string]any{
		"type": "object", "additionalProperties": false, "properties": map[string]any{
			"query":            map[string]any{"type": "string", "minLength": 1, "maxLength": 2048},
			"nearestNeighbors": map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
		}, "required": []string{"query", "nearestNeighbors"},
	}}}}
}

type toolArguments struct {
	Query            string `json:"query"`
	NearestNeighbors int    `json:"nearestNeighbors"`
}

func parseToolArguments(raw string) (toolArguments, error) {
	var args toolArguments
	if len(raw) > 4096 {
		return args, errors.New("tool arguments too large")
	}
	// Detect duplicate keys before typed decoding: encoding/json alone accepts them.
	dec := json.NewDecoder(strings.NewReader(raw))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return args, errors.New("tool arguments must be one JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return args, errors.New("invalid tool arguments")
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return args, errors.New("duplicate or invalid tool argument")
		}
		seen[name] = true
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return args, errors.New("invalid tool argument JSON")
		}
	}
	if _, err := dec.Token(); err != nil {
		return args, errors.New("invalid tool object")
	}
	dec = json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&args) != nil {
		return args, errors.New("invalid tool argument types or unknown fields")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || len(seen) != 2 {
		return args, errors.New("tool arguments require exactly query and nearestNeighbors")
	}
	return args, safety.Query(args.Query, args.NearestNeighbors)
}
