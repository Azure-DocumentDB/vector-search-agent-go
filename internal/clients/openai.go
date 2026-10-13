package clients

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/safety"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/azure"
	"github.com/openai/openai-go/v3/option"
)

type OpenAIConfig struct {
	Endpoint, APIKey, AuthMode               string
	EmbeddingDeployment, EmbeddingAPIVersion string
	PlannerDeployment, PlannerAPIVersion     string
	SynthDeployment, SynthAPIVersion         string
	Dimensions, MaxCalls                     int
	AllowInference                           bool
}

func LoadConfigFromEnv() (*OpenAIConfig, error) {
	c := &OpenAIConfig{Endpoint: os.Getenv("AZURE_OPENAI_ENDPOINT"), APIKey: os.Getenv("AZURE_OPENAI_API_KEY"), AuthMode: os.Getenv("AZURE_OPENAI_AUTH_MODE"), EmbeddingDeployment: os.Getenv("AZURE_OPENAI_EMBEDDING_DEPLOYMENT"), EmbeddingAPIVersion: os.Getenv("AZURE_OPENAI_EMBEDDING_API_VERSION"), PlannerDeployment: os.Getenv("AZURE_OPENAI_PLANNER_DEPLOYMENT"), PlannerAPIVersion: os.Getenv("AZURE_OPENAI_PLANNER_API_VERSION"), SynthDeployment: os.Getenv("AZURE_OPENAI_SYNTH_DEPLOYMENT"), SynthAPIVersion: os.Getenv("AZURE_OPENAI_SYNTH_API_VERSION"), Dimensions: 1536, MaxCalls: 53}
	for key, dst := range map[string]*int{"EMBEDDING_DIMENSIONS": &c.Dimensions, "AGENT_MAX_MODEL_CALLS": &c.MaxCalls} {
		if v := os.Getenv(key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, errors.New("invalid numeric model configuration")
			}
			*dst = n
		}
	}
	switch os.Getenv("AGENT_ALLOW_PAID_INFERENCE") {
	case "true":
		c.AllowInference = true
	case "", "false":
	default:
		return nil, errors.New("AGENT_ALLOW_PAID_INFERENCE must be true or false")
	}
	return c, c.Validate()
}

var apiVersion = regexp.MustCompile(`^20[0-9]{2}-[0-9]{2}-[0-9]{2}(-preview)?$`)

func (c OpenAIConfig) Validate() error {
	if err := safety.ModelEndpoint(c.Endpoint); err != nil {
		return err
	}
	if c.AuthMode != "key" && c.AuthMode != "entra" {
		return errors.New("AZURE_OPENAI_AUTH_MODE must explicitly be key or entra")
	}
	if c.AuthMode == "key" && (strings.TrimSpace(c.APIKey) == "" || strings.ContainsAny(c.APIKey, "\r\n")) {
		return errors.New("API key is required for key mode")
	}
	if c.AuthMode == "entra" && c.APIKey != "" {
		return errors.New("unset AZURE_OPENAI_API_KEY for entra mode; no fallback")
	}
	for _, d := range []string{c.EmbeddingDeployment, c.PlannerDeployment, c.SynthDeployment} {
		if !safety.Identifier(d) {
			return errors.New("all three deployments must be explicit simple identifiers")
		}
	}
	for _, v := range []string{c.EmbeddingAPIVersion, c.PlannerAPIVersion, c.SynthAPIVersion} {
		if !apiVersion.MatchString(v) {
			return errors.New("all three API versions must be explicit YYYY-MM-DD[-preview]")
		}
		if _, err := time.Parse("2006-01-02", strings.TrimSuffix(v, "-preview")); err != nil {
			return errors.New("invalid API version date")
		}
	}
	if c.Dimensions < 1 || c.Dimensions > 2000 || c.MaxCalls < 1 || c.MaxCalls > 53 {
		return errors.New("dimensions must be 1-2000; AGENT_MAX_MODEL_CALLS must be 1-53")
	}
	return nil
}

type OpenAIClients struct {
	config                    OpenAIConfig
	embedding, planner, synth openai.Client
	credential                azcore.TokenCredential
	mu                        sync.Mutex
	calls                     int
	closeIdle                 func()
}

func NewOpenAIClients(c *OpenAIConfig) (*OpenAIClients, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Do not silently forward secrets through ambient HTTP proxies.
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return newClients(c, transport, func() (azcore.TokenCredential, error) { return azidentity.NewDefaultAzureCredential(nil) })
}

// Private injection keeps tests local without adding an insecure endpoint switch to the CLI.
func newClients(c *OpenAIConfig, transport http.RoundTripper, factory func() (azcore.TokenCredential, error)) (*OpenAIClients, error) {
	if c == nil {
		return nil, errors.New("model configuration required")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	} // Before credential factory.
	result := &OpenAIClients{config: *c}
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		result.closeIdle = closer.CloseIdleConnections
	}
	if c.AuthMode == "entra" {
		cred, err := factory()
		if err != nil {
			return nil, safety.Protect("model credential initialization", err)
		}
		result.credential = safety.CheckedCredential{Inner: cred, Scope: safety.ModelScope}
	}
	origin, _ := url.Parse(c.Endpoint)
	client := &http.Client{Transport: pinnedTransport{next: transport, host: origin.Host}, Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	makeClient := func(version string) openai.Client {
		opts := []option.RequestOption{azure.WithEndpoint(c.Endpoint, version), option.WithHTTPClient(client), option.WithMaxRetries(0), option.WithRequestTimeout(30 * time.Second), option.WithAPIKey("")}
		if c.AuthMode == "key" {
			opts = append(opts, azure.WithAPIKey(c.APIKey))
		} else {
			opts = append(opts, azure.WithTokenCredential(result.credential))
		}
		return openai.NewClient(opts...)
	}
	result.embedding, result.planner, result.synth = makeClient(c.EmbeddingAPIVersion), makeClient(c.PlannerAPIVersion), makeClient(c.SynthAPIVersion)
	return result, nil
}

type pinnedTransport struct {
	next http.RoundTripper
	host string
}

func (t pinnedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != t.host || r.URL.User != nil {
		return nil, errors.New("model request destination rejected")
	}
	resp, err := t.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	// Bound service/error bodies before SDK decoding; always close the network body.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(body) > 4*1024*1024 {
		return nil, errors.New("model response too large")
	}
	resp.Body = io.NopCloser(strings.NewReader(string(body)))
	return resp, nil
}
func (c *OpenAIClients) Close() {
	if c.closeIdle != nil {
		c.closeIdle()
	}
}
func (c *OpenAIClients) AuthCheck(ctx context.Context) error {
	// Key syntax can be checked, but key validity cannot honestly be proved without a service call.
	// Entra acquisition proves only token issuance, not model RBAC/deployment access.
	if c.credential == nil {
		return nil
	}
	_, err := c.credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{safety.ModelScope}})
	return err
}
func (c *OpenAIClients) reserve(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return safety.Protect("model operation", err)
	}
	if !c.config.AllowInference {
		return errors.New("paid model calls disabled; require AGENT_ALLOW_PAID_INFERENCE=true")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls >= c.config.MaxCalls {
		return errors.New("model request budget exhausted")
	}
	c.calls++
	return nil
}
func (c *OpenAIClients) Calls() int { c.mu.Lock(); defer c.mu.Unlock(); return c.calls }
func (c *OpenAIClients) GenerateEmbedding(ctx context.Context, text string) ([]float32, error) {
	if strings.TrimSpace(text) == "" || len(text) > 8192 {
		return nil, errors.New("embedding input must be nonempty and at most 8192 bytes")
	}
	if err := c.reserve(ctx); err != nil {
		return nil, err
	}
	resp, err := c.embedding.Embeddings.New(ctx, openai.EmbeddingNewParams{Input: openai.EmbeddingNewParamsInputUnion{OfString: openai.String(text)}, Model: openai.EmbeddingModel(c.config.EmbeddingDeployment), EncodingFormat: openai.EmbeddingNewParamsEncodingFormatFloat})
	if err != nil {
		return nil, safety.Protect("embedding request", err)
	}
	if resp == nil || len(resp.Data) != 1 || resp.Data[0].Index != 0 {
		return nil, errors.New("embedding response must contain exactly one indexed vector")
	}
	values := resp.Data[0].Embedding
	vector := make([]float32, len(values))
	for i, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > math.MaxFloat32 {
			return nil, errors.New("invalid embedding value")
		}
		vector[i] = float32(v)
	}
	if err := safety.Vector(vector, c.config.Dimensions); err != nil {
		return nil, err
	}
	return vector, nil
}
func (c *OpenAIClients) Plan(ctx context.Context, system, user string, tool openai.ChatCompletionToolUnionParam) (*openai.ChatCompletion, error) {
	if len(user) > 4096 {
		return nil, errors.New("planner input too large")
	}
	if err := c.reserve(ctx); err != nil {
		return nil, err
	}
	resp, err := c.planner.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{Model: openai.ChatModel(c.config.PlannerDeployment), Messages: []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(system), openai.UserMessage(user)}, Tools: []openai.ChatCompletionToolUnionParam{tool}, ToolChoice: openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: openai.String("required")}, ParallelToolCalls: openai.Bool(false), Temperature: openai.Float(0), MaxTokens: openai.Int(600)})
	if err != nil {
		return nil, safety.Protect("planner request", err)
	}
	if resp == nil || len(resp.Choices) != 1 || resp.Choices[0].FinishReason != "tool_calls" || len(resp.Choices[0].Message.ToolCalls) != 1 {
		return nil, errors.New("planner must return exactly one complete tool call")
	}
	call := resp.Choices[0].Message.ToolCalls[0]
	if call.Type != "function" || !safety.Identifier(call.ID) || len(call.Function.Arguments) > 4096 {
		return nil, errors.New("planner returned invalid tool call")
	}
	return resp, nil
}
func (c *OpenAIClients) Synthesize(ctx context.Context, system, query, summary string, plan *openai.ChatCompletion) (string, error) {
	if plan == nil || len(plan.Choices) != 1 || len(plan.Choices[0].Message.ToolCalls) != 1 || len(summary) == 0 || len(summary) > 65536 {
		return "", errors.New("invalid synthesis context")
	}
	if err := safety.Query(query, 1); err != nil {
		return "", err
	}
	if err := c.reserve(ctx); err != nil {
		return "", err
	}
	callID := plan.Choices[0].Message.ToolCalls[0].ID
	resp, err := c.synth.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{Model: openai.ChatModel(c.config.SynthDeployment), Messages: []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(system), openai.UserMessage(query), plan.Choices[0].Message.ToParam(), openai.ToolMessage(summary, callID)}, MaxTokens: openai.Int(600), Temperature: openai.Float(0.3)})
	if err != nil {
		return "", safety.Protect("synthesizer request", err)
	}
	if resp == nil || len(resp.Choices) != 1 || resp.Choices[0].FinishReason != "stop" || len(resp.Choices[0].Message.ToolCalls) != 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" || len(resp.Choices[0].Message.Content) > 8192 {
		return "", errors.New("synthesizer returned empty, incomplete, or invalid output")
	}
	return resp.Choices[0].Message.Content, nil
}
