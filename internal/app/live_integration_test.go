//go:build integration

package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/agents"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/clients"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/models"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/vectorstore"
)

// This gate has NO mock or offline fallback. It intentionally fails, not skips,
// without explicit opt-in, and permits only this one fixed disposable namespace.
func TestLiveAgent(t *testing.T) {
	if os.Getenv("AGENT_LIVE_TEST") != "1" {
		t.Fatal("requires AGENT_LIVE_TEST=1; no network calls made")
	}
	db, err := vectorstore.LoadConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if db.DatabaseName != vectorstore.LiveDatabase || db.CollectionName != vectorstore.CollectionName || db.Algorithm != "vector-ivf" {
		t.Fatal("live gate requires agent_go_disposable_live_gate.agent_hotels with vector-ivf")
	}
	if err := db.Cleanup(); err != nil {
		t.Fatal(err)
	}
	config, err := clients.LoadConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !config.AllowInference || config.Dimensions != 1536 || config.MaxCalls < 6 || config.MaxCalls > 9 {
		t.Fatal("live gate requires paid opt-in, 1536 dimensions, and AGENT_MAX_MODEL_CALLS between 6 and 9")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store, err := vectorstore.NewVectorStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	ids, err := store.InspectOwned(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatal("live gate requires initially empty owned collection; run explicit scoped cleanup first")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := store.Cleanup(cleanup); err != nil {
			t.Error("live cleanup failed:", err)
		}
	}()
	model, err := clients.NewOpenAIClients(config)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	if err := model.AuthCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if model.Calls() != 0 {
		t.Fatal("auth check made an inference request")
	}
	hotels := []models.Hotel{
		{HotelID: "live-trails", HotelName: "Trail House", Description: "Quiet hotel beside forest running trails with parking and breakfast.", Category: "Nature", Rating: 4.7},
		{HotelID: "live-city", HotelName: "City Rooms", Description: "Downtown hotel near restaurants, shopping and public transit.", Category: "Urban", Rating: 4.2},
		{HotelID: "live-beach", HotelName: "Beach Lodge", Description: "Beachfront lodge with a swimming pool and ocean views.", Category: "Beach", Rating: 4.4},
	}
	// Genuine Azure embeddings are generated once and reused to prove idempotent seeding.
	embedded := make([]models.HotelForVectorStore, 0, len(hotels))
	for _, h := range hotels {
		v, err := model.GenerateEmbedding(ctx, h.Description)
		if err != nil {
			t.Fatal(err)
		}
		item := h.ToVectorStore()
		item.DescriptionVector = v
		embedded = append(embedded, item)
	}
	for range 2 {
		if err := store.InsertHotelsWithEmbeddings(ctx, embedded); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateVectorIndex(ctx); err != nil {
		t.Fatal(err)
	}
	ids, err = store.InspectOwned(ctx)
	if err != nil || len(ids) != 3 {
		t.Fatal("idempotent seed did not retain exactly 3 rows", err)
	}
	answer, err := agents.New(model, agents.NewVectorSearchTool(model, store)).Run(ctx, "Recommend a quiet hotel near running trails, compare available options", 3)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(answer) == "" || (!strings.Contains(answer, "Trail House") && !strings.Contains(answer, "City Rooms") && !strings.Contains(answer, "Beach Lodge")) {
		t.Fatal("genuine synthesis did not reference any retrieved hotel")
	}
	if model.Calls() != 6 {
		t.Fatalf("expected 3 seed embeddings + planner + query embedding + synthesis, got %d", model.Calls())
	}
	t.Logf("LIVE PASS: DB auth=%s model auth=%s; 3 real 1536D seed embeddings, repeat seed remains 3; real planner/tool/query embedding/DB search/synthesis; 6 total inference requests. Scoped cleanup follows.", db.AuthMode, config.AuthMode)
}
