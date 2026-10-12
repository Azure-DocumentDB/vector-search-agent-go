package agents

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/prompts"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/safety"
	"github.com/openai/openai-go/v3"
)

// The sample deliberately orchestrates directly; it does not claim an external framework.
type Model interface {
	Plan(context.Context, string, string, openai.ChatCompletionToolUnionParam) (*openai.ChatCompletion, error)
	Synthesize(context.Context, string, string, string, *openai.ChatCompletion) (string, error)
}
type Agent struct {
	model Model
	tool  *VectorSearchTool
}

func New(model Model, tool *VectorSearchTool) *Agent { return &Agent{model, tool} }
func (a *Agent) Run(ctx context.Context, query string, k int) (string, error) {
	if err := safety.Query(query, k); err != nil {
		return "", err
	}
	// Serialize user input as data, never concatenate it into an instruction template.
	user, _ := json.Marshal(toolArguments{Query: query, NearestNeighbors: k})
	plan, err := a.model.Plan(ctx, prompts.PlannerSystemPrompt, string(user), a.tool.GetToolDefinition())
	if err != nil {
		return "", err
	}
	if plan == nil || len(plan.Choices) != 1 || plan.Choices[0].FinishReason != "tool_calls" || len(plan.Choices[0].Message.ToolCalls) != 1 {
		return "", errors.New("planner must return exactly one complete tool call")
	}
	call := plan.Choices[0].Message.ToolCalls[0]
	if call.Type != "function" || call.Function.Name != prompts.ToolName || !safety.Identifier(call.ID) {
		return "", errors.New("unknown or invalid planner tool")
	}
	args, err := parseToolArguments(call.Function.Arguments)
	if err != nil {
		return "", err
	}
	if args.NearestNeighbors > k {
		return "", errors.New("planner exceeded requested result budget")
	}
	summary, err := a.tool.Execute(ctx, args.Query, args.NearestNeighbors)
	if err != nil {
		return "", err
	}
	return a.model.Synthesize(ctx, prompts.SynthesizerSystemPrompt, query, summary, plan)
}
