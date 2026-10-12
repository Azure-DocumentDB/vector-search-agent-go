package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/agents"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/models"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/safety"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/openai/openai-go/v3"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig() *OpenAIConfig {
	return &OpenAIConfig{Endpoint: "https://unit.openai.azure.com/", APIKey: "synthetic-key", AuthMode: "key", EmbeddingDeployment: "embed", EmbeddingAPIVersion: "2023-05-15", PlannerDeployment: "plan", PlannerAPIVersion: "2024-10-21", SynthDeployment: "synth", SynthAPIVersion: "2025-04-01-preview", Dimensions: 3, MaxCalls: 53, AllowInference: true}
}

// Private test transport retains the SDK-built Azure request and changes only the
// physical destination to an isolated local server; production has no such override.
type rewriteTransport struct {
	next   http.RoundTripper
	target *url.URL
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = r.target.Scheme
	u.Host = r.target.Host
	copy.URL = &u
	copy.Host = req.URL.Host
	return r.next.RoundTrip(copy)
}

type tokenSource struct {
	calls    atomic.Int32
	expires  time.Time
	token    string
	badScope atomic.Bool
}

func (s *tokenSource) GetToken(_ context.Context, p policy.TokenRequestOptions) (azcore.AccessToken, error) {
	s.calls.Add(1)
	if len(p.Scopes) != 1 || p.Scopes[0] != safety.ModelScope {
		s.badScope.Store(true)
	}
	return azcore.AccessToken{Token: s.token, ExpiresOn: s.expires}, nil
}
func localClient(t *testing.T, c *OpenAIConfig, h http.HandlerFunc, token *tokenSource) *OpenAIClients {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	client, err := newClients(c, rewriteTransport{next: server.Client().Transport, target: u}, func() (azcore.TokenCredential, error) {
		if token == nil {
			t.Fatal("unexpected credential construction")
		}
		return token, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

const plannerJSON = `{"id":"plan1","object":"chat.completion","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_hotels_collection","arguments":"{\"query\":\"quiet trails\",\"nearestNeighbors\":3}"}}]}}]}`
const synthJSON = `{"id":"synth1","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Choose Trail House for nearby trails."}}]}`
const embeddingJSON = `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0,0]}],"model":"embed","usage":{"prompt_tokens":2,"total_tokens":2}}`

func planResponse(t *testing.T) *openai.ChatCompletion {
	t.Helper()
	var p openai.ChatCompletion
	if err := json.Unmarshal([]byte(plannerJSON), &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

type mockSearch struct{ calls int }

func (s *mockSearch) VectorSearch(_ context.Context, v []float32, k int) ([]models.HotelSearchResult, error) {
	s.calls++
	if len(v) != 3 || v[0] != 1 || k != 3 {
		return nil, errors.New("bad search contract")
	}
	return []models.HotelSearchResult{{Hotel: models.HotelForVectorStore{HotelID: "1", HotelName: "Trail House", Description: "Quiet trails"}, Score: 1}}, nil
}
func TestAzureRESTAndActualOrchestrationBothAuthModes(t *testing.T) {
	for _, mode := range []string{"key", "entra"} {
		t.Run(mode, func(t *testing.T) {
			c := testConfig()
			c.AuthMode = mode
			token := &tokenSource{token: "synthetic-token", expires: time.Now().Add(time.Hour)}
			if mode == "entra" {
				c.APIKey = ""
			}
			var calls atomic.Int32
			client := localClient(t, c, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if r.Method != "POST" || r.Host != "unit.openai.azure.com" {
					t.Error("wrong method/host")
				}
				if mode == "key" {
					if r.Header.Get("api-key") != "synthetic-key" || r.Header.Get("Authorization") != "" {
						t.Error("wrong API-key headers")
					}
				} else {
					if r.Header.Get("Authorization") != "Bearer synthetic-token" || r.Header.Get("api-key") != "" {
						t.Error("wrong bearer headers")
					}
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid request JSON")
					return
				}
				switch r.URL.Path {
				case "/openai/deployments/embed/embeddings":
					if r.URL.Query().Get("api-version") != c.EmbeddingAPIVersion || body["model"] != "embed" || body["input"] != "quiet trails" || body["encoding_format"] != "float" {
						t.Error("embedding URL/version/body contract")
					}
					fmt.Fprint(w, embeddingJSON)
				case "/openai/deployments/plan/chat/completions":
					if r.URL.Query().Get("api-version") != c.PlannerAPIVersion || body["model"] != "plan" || body["tool_choice"] != "required" || body["parallel_tool_calls"] != false || body["max_tokens"] != float64(600) {
						t.Error("planner URL/version/budget contract")
					}
					tools := body["tools"].([]any)
					function := tools[0].(map[string]any)["function"].(map[string]any)
					if function["name"] != "search_hotels_collection" || function["strict"] != true {
						t.Error("tool contract")
					}
					fmt.Fprint(w, plannerJSON)
				case "/openai/deployments/synth/chat/completions":
					if r.URL.Query().Get("api-version") != c.SynthAPIVersion || body["model"] != "synth" || body["max_tokens"] != float64(600) {
						t.Error("synthesis URL/version/budget contract")
					}
					messages := body["messages"].([]any)
					if len(messages) != 4 {
						t.Error("missing tool conversation")
						return
					}
					assistant := messages[2].(map[string]any)
					tool := messages[3].(map[string]any)
					if assistant["role"] != "assistant" || tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || !strings.Contains(tool["content"].(string), "Trail House") {
						t.Error("tool call/result conversation mismatch")
					}
					fmt.Fprint(w, synthJSON)
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
					http.Error(w, "wrong route", 404)
				}
			}, token)
			if err := client.AuthCheck(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 0 {
				t.Fatal("auth check called model")
			}
			store := &mockSearch{}
			answer, err := agents.New(client, agents.NewVectorSearchTool(client, store)).Run(context.Background(), "hotel near trails", 3)
			if err != nil || !strings.Contains(answer, "Trail House") || calls.Load() != 3 || store.calls != 1 || client.Calls() != 3 {
				t.Fatalf("orchestration: answer=%q err=%v requests=%d", answer, err, calls.Load())
			}
			if token.badScope.Load() {
				t.Fatal("bad token audience")
			}
		})
	}
}
func TestModelFailureContracts(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, `{"error":{"message":"synthetic-sensitive-body"}}`}, {"rate_limit", 429, `{"error":{"message":"secret"}}`}, {"server_error", 500, `{"error":{"message":"secret"}}`}, {"malformed", 200, `not json`}, {"empty", 200, `{"data":[]}`}, {"wrong_dimension", 200, `{"data":[{"index":0,"embedding":[1]}]}`}, {"zero_vector", 200, `{"data":[{"index":0,"embedding":[0,0,0]}]}`}, {"multiple_vectors", 200, `{"data":[{"index":0,"embedding":[1,0,0]},{"index":1,"embedding":[1,0,0]}]}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var n atomic.Int32
			client := localClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}, nil)
			_, err := client.GenerateEmbedding(context.Background(), "hotel")
			if err == nil || strings.Contains(err.Error(), "synthetic-sensitive") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe or missing error: %v", err)
			}
			if n.Load() != 1 {
				t.Fatal("automatic retry exceeded budget")
			}
		})
	}
}
func TestPlannerAndSynthesisRejectInvalidResponses(t *testing.T) {
	for _, body := range []string{`{"choices":[]}`, `{"choices":[{"finish_reason":"stop","message":{"content":"no tool"}}]}`, strings.Replace(plannerJSON, `"tool_calls","message"`, `"length","message"`, 1)} {
		client := localClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, body)
		}, nil)
		if _, err := client.Plan(context.Background(), "system", "query", agents.NewVectorSearchTool(nil, nil).GetToolDefinition()); err == nil {
			t.Fatal("bad planner accepted")
		}
	}
	for _, body := range []string{`{"choices":[]}`, `{"choices":[{"finish_reason":"stop","message":{"content":" "}}]}`, strings.Replace(synthJSON, `"stop"`, `"length"`, 1), strings.Replace(synthJSON, `"stop"`, `"content_filter"`, 1)} {
		client := localClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, body)
		}, nil)
		if _, err := client.Synthesize(context.Background(), "system", "query", "context", planResponse(t)); err == nil {
			t.Fatal("bad synth accepted")
		}
	}
}
func TestDestinationBeforeCredentialAndExpiredToken(t *testing.T) {
	c := testConfig()
	c.AuthMode = "entra"
	c.APIKey = ""
	c.Endpoint = "https://evil.test/"
	called := false
	if _, err := newClients(c, http.DefaultTransport, func() (azcore.TokenCredential, error) { called = true; return nil, nil }); err == nil || called {
		t.Fatal("credential constructed before validation")
	}
	c.Endpoint = "https://unit.openai.azure.com/"
	var requests atomic.Int32
	token := &tokenSource{token: "expired", expires: time.Now().Add(-time.Hour)}
	client := localClient(t, c, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }, token)
	if _, err := client.GenerateEmbedding(context.Background(), "hotel"); err == nil || requests.Load() != 0 {
		t.Fatal("expired token sent")
	}
}
func TestRedirectBudgetCancellationAndOptIn(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	client := localClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}, nil)
	if _, err := client.GenerateEmbedding(context.Background(), "hotel"); err == nil || targetCalls.Load() != 0 {
		t.Fatal("redirect followed")
	}
	c := testConfig()
	c.MaxCalls = 2
	client = localClient(t, c, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, embeddingJSON)
	}, nil)
	var wg sync.WaitGroup
	var successes atomic.Int32
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.GenerateEmbedding(context.Background(), "hotel"); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 2 || client.Calls() != 2 {
		t.Fatal("budget race")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.GenerateEmbedding(ctx, "hotel"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not preserved")
	}
	c = testConfig()
	c.AllowInference = false
	client = localClient(t, c, func(w http.ResponseWriter, r *http.Request) { t.Error("inference opt-in bypassed") }, nil)
	if _, err := client.GenerateEmbedding(context.Background(), "hotel"); err == nil || client.Calls() != 0 {
		t.Fatal("missing opt-in accepted")
	}
	client = localClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(150 * time.Millisecond):
		}
	}, nil)
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := client.GenerateEmbedding(ctx, "hotel"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout lost: %v", err)
	}
}

func TestConfigurationRejectsAmbiguityAndInvalidBounds(t *testing.T) {
	for _, mutate := range []func(*OpenAIConfig){func(c *OpenAIConfig) { c.AuthMode = "auto" }, func(c *OpenAIConfig) { c.AuthMode = "entra" }, func(c *OpenAIConfig) { c.APIKey = "" }, func(c *OpenAIConfig) { c.EmbeddingDeployment = "../evil" }, func(c *OpenAIConfig) { c.PlannerAPIVersion = "2024-99-99" }, func(c *OpenAIConfig) { c.SynthAPIVersion = "" }, func(c *OpenAIConfig) { c.Dimensions = 2001 }, func(c *OpenAIConfig) { c.MaxCalls = 54 }} {
		c := testConfig()
		mutate(c)
		if c.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
func TestAmbientOpenAISettingsCannotOverrideExplicitAzure(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-synthetic")
	t.Setenv("OPENAI_BASE_URL", "https://evil.test/")
	t.Setenv("OPENAI_ORG_ID", "ambient-org")
	t.Setenv("OPENAI_PROJECT_ID", "ambient-project")
	client := localClient(t, testConfig(), func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "unit.openai.azure.com" || r.Header.Get("Authorization") != "" || r.Header.Get("api-key") != "synthetic-key" {
			t.Error("ambient credential or destination inherited")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, embeddingJSON)
	}, nil)
	if _, err := client.GenerateEmbedding(context.Background(), "hotel"); err != nil {
		t.Fatal(err)
	}
}
