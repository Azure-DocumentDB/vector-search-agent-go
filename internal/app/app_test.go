package app

import (
	"bytes"
	"context"
	"errors"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/clients"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/models"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/vectorstore"
	"github.com/openai/openai-go/v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type storeFake struct {
	inserts, indexes, closes, cleanups int
	failClose                          bool
}

func (s *storeFake) InspectOwned(context.Context) ([]string, error) { return nil, nil }
func (s *storeFake) InsertHotelsWithEmbeddings(context.Context, []models.HotelForVectorStore) error {
	s.inserts++
	return nil
}
func (s *storeFake) CreateVectorIndex(context.Context) error { s.indexes++; return nil }
func (s *storeFake) Cleanup(context.Context) error           { s.cleanups++; return nil }
func (s *storeFake) Close() error {
	s.closes++
	if s.failClose {
		return errors.New("close failed")
	}
	return nil
}
func (s *storeFake) VectorSearch(context.Context, []float32, int) ([]models.HotelSearchResult, error) {
	return nil, errors.New("unused")
}

type modelFake struct {
	embeds, auth, closes int
	failAt               int
}

func (m *modelFake) GenerateEmbedding(context.Context, string) ([]float32, error) {
	m.embeds++
	if m.embeds == m.failAt {
		return nil, errors.New("HTTP 401 withheld")
	}
	return []float32{1, 0, 0}, nil
}
func (m *modelFake) Plan(context.Context, string, string, openai.ChatCompletionToolUnionParam) (*openai.ChatCompletion, error) {
	return nil, errors.New("planner failed")
}
func (m *modelFake) Synthesize(context.Context, string, string, string, *openai.ChatCompletion) (string, error) {
	return "", errors.New("unused")
}
func (m *modelFake) AuthCheck(context.Context) error { m.auth++; return nil }
func (m *modelFake) Close()                          { m.closes++ }
func setEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"USE_PASSWORDLESS", "AZURE_DOCUMENTDB_CONNECTION_STRING", "AZURE_OPENAI_API_KEY", "EMBEDDED_FIELD", "AZURE_DOCUMENTDB_INDEX_NAME", "AGENT_TIMEOUT", "IVF_NUM_LISTS", "HNSW_M", "HNSW_EF_CONSTRUCTION", "DISKANN_MAX_DEGREE", "DISKANN_L_BUILD", "VECTOR_SIMILARITY"} {
		t.Setenv(key, "")
	}
	for k, v := range map[string]string{"AZURE_DOCUMENTDB_HOST": "unit.global.mongocluster.cosmos.azure.com", "AZURE_DOCUMENTDB_AUTH_MODE": "entra", "AZURE_DOCUMENTDB_DATABASENAME": "agent_go_disposable_test", "AZURE_DOCUMENTDB_COLLECTION": "agent_hotels", "AGENT_DISPOSABLE_TARGET": "agent_go_disposable_test.agent_hotels", "AGENT_CONFIRM_CLEANUP": "agent_go_disposable_test.agent_hotels", "AGENT_ALLOW_WRITES": "true", "AZURE_OPENAI_AUTH_MODE": "entra", "AZURE_OPENAI_ENDPOINT": "https://unit.openai.azure.com/", "AZURE_OPENAI_EMBEDDING_DEPLOYMENT": "embed", "AZURE_OPENAI_PLANNER_DEPLOYMENT": "plan", "AZURE_OPENAI_SYNTH_DEPLOYMENT": "synth", "AZURE_OPENAI_EMBEDDING_API_VERSION": "2024-10-21", "AZURE_OPENAI_PLANNER_API_VERSION": "2024-10-21", "AZURE_OPENAI_SYNTH_API_VERSION": "2024-10-21", "AGENT_ALLOW_PAID_INFERENCE": "true", "AGENT_MAX_MODEL_CALLS": "53", "EMBEDDING_DIMENSIONS": "3", "NEAREST_NEIGHBORS": "3", "QUERY": "hotel near trails"} {
		t.Setenv(k, v)
	}
}
func TestUploadAnyEmbeddingFailurePreventsAllWrites(t *testing.T) {
	hotels := []models.Hotel{{HotelID: "1", HotelName: "One", Description: "one"}, {HotelID: "2", HotelName: "Two", Description: "two"}}
	for _, failAt := range []int{1, 2} {
		m := &modelFake{failAt: failAt}
		s := &storeFake{}
		if Upload(context.Background(), m, s, hotels) == nil || s.inserts != 0 || s.indexes != 0 {
			t.Fatal("failed embeddings caused success/writes")
		}
	}
	m := &modelFake{}
	s := &storeFake{}
	if Upload(context.Background(), m, s, hotels) != nil || m.embeds != 2 || s.inserts != 1 || s.indexes != 1 {
		t.Fatal("valid upload did not complete")
	}
}
func TestAuthCheckNoInferenceAndCleanupNoModel(t *testing.T) {
	setEnv(t)
	for _, cmd := range []string{"auth-check", "cleanup"} {
		m := &modelFake{}
		s := &storeFake{}
		deps := Dependencies{Store: func(context.Context, *vectorstore.VectorStoreConfig) (Store, error) { return s, nil }, Model: func(*clients.OpenAIConfig) (Model, error) {
			if cmd == "cleanup" {
				t.Fatal("cleanup constructed model")
			}
			return m, nil
		}}
		var out bytes.Buffer
		if err := Run(context.Background(), cmd, &out, deps); err != nil {
			t.Fatal(err)
		}
		if s.closes != 1 || m.embeds != 0 {
			t.Fatal("resource leak or paid auth check")
		}
		if cmd == "auth-check" && (m.auth != 1 || m.closes != 1 || !strings.Contains(out.String(), "NOT proved")) {
			t.Fatal("auth check misrepresented proof")
		}
	}
}
func TestPreflightBeforeAnyFactory(t *testing.T) {
	setEnv(t)
	deps := Dependencies{Store: func(context.Context, *vectorstore.VectorStoreConfig) (Store, error) {
		t.Fatal("factory before validation")
		return nil, nil
	}}
	for k, v := range map[string]string{"AZURE_OPENAI_ENDPOINT": "https://evil.test/", "NEAREST_NEIGHBORS": "NaN", "AGENT_ALLOW_PAID_INFERENCE": "false", "AGENT_TIMEOUT": "0s", "AGENT_MAX_MODEL_CALLS": "2"} {
		old := os.Getenv(k)
		t.Setenv(k, v)
		if Run(context.Background(), "agent", &bytes.Buffer{}, deps) == nil {
			t.Fatal("invalid preflight accepted")
		}
		t.Setenv(k, old)
	}
}
func TestRunUpload401NonSuccessAndResourceCleanup(t *testing.T) {
	setEnv(t)
	path := filepath.Join(t.TempDir(), "data.json")
	if os.WriteFile(path, []byte(`[{"HotelId":"1","HotelName":"One","Description":"quiet"}]`), 0600) != nil {
		t.Fatal("fixture")
	}
	t.Setenv("DATA_FILE_WITHOUT_VECTORS", path)
	m := &modelFake{failAt: 1}
	s := &storeFake{}
	deps := Dependencies{Store: func(context.Context, *vectorstore.VectorStoreConfig) (Store, error) { return s, nil }, Model: func(*clients.OpenAIConfig) (Model, error) { return m, nil }}
	var out bytes.Buffer
	if Run(context.Background(), "upload", &out, deps) == nil || strings.Contains(out.String(), "succeeded") || s.inserts != 0 || s.closes != 1 || m.closes != 1 {
		t.Fatal("upload false success or resource leak")
	}
	s.failClose = true
	m.failAt = 0
	if Run(context.Background(), "auth-check", &out, deps) == nil {
		t.Fatal("disconnect error swallowed")
	}
}
func TestMainReturnsNonzeroInvalidConfiguration(t *testing.T) {
	t.Setenv("AZURE_DOCUMENTDB_DATABASENAME", "")
	for _, cmd := range []string{"agent", "upload", "cleanup", "auth-check"} {
		var out, stderr bytes.Buffer
		if Main(cmd, &out, &stderr) != 1 || out.Len() != 0 || stderr.Len() == 0 {
			t.Fatal("entrypoint false success")
		}
	}
}
