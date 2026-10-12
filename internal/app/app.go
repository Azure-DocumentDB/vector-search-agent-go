package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/agents"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/clients"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/models"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/safety"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/vectorstore"
)

type Store interface {
	agents.SearchStore
	InspectOwned(context.Context) ([]string, error)
	InsertHotelsWithEmbeddings(context.Context, []models.HotelForVectorStore) error
	CreateVectorIndex(context.Context) error
	Cleanup(context.Context) error
	Close() error
}
type Model interface {
	agents.Model
	agents.Embedder
	AuthCheck(context.Context) error
	Close()
}
type Dependencies struct {
	Store func(context.Context, *vectorstore.VectorStoreConfig) (Store, error)
	Model func(*clients.OpenAIConfig) (Model, error)
}

func Production() Dependencies {
	return Dependencies{
		Store: func(ctx context.Context, c *vectorstore.VectorStoreConfig) (Store, error) {
			return vectorstore.NewVectorStore(ctx, c)
		},
		Model: func(c *clients.OpenAIConfig) (Model, error) { return clients.NewOpenAIClients(c) },
	}
}

// Main returns after run/cleanup; only the tiny executable wrapper calls os.Exit.
func Main(command string, out, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := Run(ctx, command, out, Production()); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}
func Run(parent context.Context, command string, out io.Writer, deps Dependencies) (err error) {
	if command != "agent" && command != "upload" && command != "cleanup" && command != "auth-check" {
		return errors.New("unknown command")
	}
	// Read process environment only; no implicit credential files or dotenv execution.
	db, err := vectorstore.LoadConfigFromEnv()
	if err != nil {
		return err
	}
	if command == "cleanup" {
		if err := db.Cleanup(); err != nil {
			return err
		}
	}
	if command == "upload" {
		if err := db.Writes(); err != nil {
			return err
		}
	}
	timeout := 5 * time.Minute
	if v := os.Getenv("AGENT_TIMEOUT"); v != "" {
		timeout, err = time.ParseDuration(v)
		if err != nil || timeout < time.Second || timeout > 10*time.Minute {
			return errors.New("AGENT_TIMEOUT must be 1s-10m")
		}
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var modelConfig *clients.OpenAIConfig
	var hotels []models.Hotel
	query := os.Getenv("QUERY")
	k := 5
	if v := os.Getenv("NEAREST_NEIGHBORS"); v != "" {
		k, err = strconv.Atoi(v)
		if err != nil {
			return errors.New("NEAREST_NEIGHBORS must be an integer")
		}
	}
	if command != "cleanup" {
		modelConfig, err = clients.LoadConfigFromEnv()
		if err != nil {
			return err
		}
		if command != "auth-check" && !modelConfig.AllowInference {
			return errors.New("paid calls disabled; require AGENT_ALLOW_PAID_INFERENCE=true")
		}
		if command == "agent" {
			if err := safety.Query(query, k); err != nil {
				return err
			}
			if modelConfig.MaxCalls < 3 {
				return errors.New("agent requires a budget of at least 3 model calls")
			}
		}
		if command == "upload" {
			path := os.Getenv("DATA_FILE_WITHOUT_VECTORS")
			if path == "" {
				path = "data/Hotels.json"
			}
			hotels, err = vectorstore.LoadHotelsFromJSON(path)
			if err != nil {
				return err
			}
			if modelConfig.MaxCalls < len(hotels) {
				return errors.New("model budget cannot cover upload; no calls made")
			}
		}
	}
	// All configuration/data validation precedes both credential factories.
	store, err := deps.Store(ctx, db)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	if command == "cleanup" {
		if err := store.Cleanup(ctx); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "Scoped sample documents removed; database, collection and indexes preserved.")
		return err
	}
	model, err := deps.Model(modelConfig)
	if err != nil {
		return err
	}
	defer model.Close()
	if command == "auth-check" {
		if err := model.AuthCheck(ctx); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "Database ping/auth succeeded. Model configuration checked; Entra token acquisition checked if selected. NO model inference called. API-key validity and model deployment/RBAC access are NOT proved.")
		return err
	}
	if command == "upload" {
		if _, err := store.InspectOwned(ctx); err != nil {
			return err
		}
		if err := Upload(ctx, model, store, hotels); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Upload succeeded: %d hotel IDs ensured without replacing existing data.\n", len(hotels))
		return err
	}
	answer, err := agents.New(model, agents.NewVectorSearchTool(model, store)).Run(ctx, query, k)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, answer)
	return err
}
func Upload(ctx context.Context, model agents.Embedder, store Store, hotels []models.Hotel) error {
	if len(hotels) == 0 || len(hotels) > 50 {
		return errors.New("upload requires 1-50 hotels")
	}
	embedded := make([]models.HotelForVectorStore, 0, len(hotels))
	for _, h := range hotels {
		vector, err := model.GenerateEmbedding(ctx, h.Description)
		if err != nil {
			return err
		}
		item := h.ToVectorStore()
		item.DescriptionVector = vector
		embedded = append(embedded, item)
	}
	// Fail-fast/all-embeddings-first: any failed embedding means zero database writes.
	if err := store.InsertHotelsWithEmbeddings(ctx, embedded); err != nil {
		return err
	}
	return store.CreateVectorIndex(ctx)
}
