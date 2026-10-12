package vectorstore

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/models"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/safety"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/drivertest"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validConfig() VectorStoreConfig {
	return VectorStoreConfig{Host: "unit.global.mongocluster.cosmos.azure.com", AuthMode: "entra", DatabaseName: "agent_go_disposable_test", CollectionName: CollectionName, DisposableTarget: "agent_go_disposable_test.agent_hotels", CleanupTarget: "agent_go_disposable_test.agent_hotels", AllowWrites: true, Algorithm: "vector-ivf", Dimensions: 3}
}
func hotel() models.HotelForVectorStore {
	return models.HotelForVectorStore{HotelID: "1", HotelName: "Trail House", Description: "Quiet trail access", DescriptionVector: []float32{1, 0, 0}}
}
func doc(h models.HotelForVectorStore) bson.D {
	r := record{ID: "agent-go:" + h.HotelID, Owner: Owner, Digest: digest(h), Hotel: h}
	raw, _ := bson.Marshal(r)
	var d bson.D
	_ = bson.Unmarshal(raw, &d)
	return d
}
func cursorResponse(docs ...bson.D) bson.D {
	items := bson.A{}
	for _, d := range docs {
		items = append(items, d)
	}
	return bson.D{{Key: "ok", Value: 1}, {Key: "cursor", Value: bson.D{{Key: "id", Value: int64(0)}, {Key: "ns", Value: "agent_go_disposable_test.agent_hotels"}, {Key: "firstBatch", Value: items}}}}
}
func okResponse(n int) bson.D { return bson.D{{Key: "ok", Value: 1}, {Key: "n", Value: n}} }
func mockStore(t *testing.T, responses ...bson.D) (*VectorStore, *[]event.CommandStartedEvent) {
	t.Helper()
	deployment := drivertest.NewMockDeployment(responses...)
	events := &[]event.CommandStartedEvent{}
	opts := options.Client().SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		copy := *e
		copy.Command = append(bson.Raw(nil), e.Command...)
		*events = append(*events, copy)
	}})
	opts.Deployment = deployment
	client, err := mongo.Connect(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	c := validConfig()
	return &VectorStore{config: c, client: client, database: client.Database(c.DatabaseName), collection: client.Database(c.DatabaseName).Collection(c.CollectionName)}, events
}
func TestConfigGuardsBeforeCredentials(t *testing.T) {
	cases := []func(*VectorStoreConfig){func(c *VectorStoreConfig) { c.DatabaseName = "Hotels" }, func(c *VectorStoreConfig) { c.CollectionName = "hotels" }, func(c *VectorStoreConfig) { c.DisposableTarget = "" }, func(c *VectorStoreConfig) { c.Host = "evil.test" }, func(c *VectorStoreConfig) { c.AuthMode = "auto" }, func(c *VectorStoreConfig) { c.ConnectionString = "secret" }, func(c *VectorStoreConfig) { c.Dimensions = 0 }, func(c *VectorStoreConfig) { c.Algorithm = "typo" }}
	for _, mutate := range cases {
		c := validConfig()
		mutate(&c)
		called := false
		_, err := newVectorStore(context.Background(), &c, func() (azcore.TokenCredential, error) { called = true; return nil, nil })
		if err == nil || called {
			t.Fatal("unsafe config reached credential construction")
		}
	}
	c := validConfig()
	c.AllowWrites = false
	if c.Writes() == nil || c.Cleanup() == nil {
		t.Fatal("write consent missing")
	}
	c.AllowWrites = true
	c.CleanupTarget = ""
	if c.Cleanup() == nil {
		t.Fatal("cleanup confirmation missing")
	}
}

type dbToken struct {
	scope  string
	expiry time.Time
}

func (s *dbToken) GetToken(_ context.Context, p policy.TokenRequestOptions) (azcore.AccessToken, error) {
	s.scope = strings.Join(p.Scopes, ",")
	return azcore.AccessToken{Token: "synthetic-token", ExpiresOn: s.expiry}, nil
}
func TestDatabaseAuthTLSAudienceAndExpiry(t *testing.T) {
	c := validConfig()
	token := &dbToken{expiry: time.Now().Add(time.Hour)}
	opts, err := configureOptions(options.Client(), c, token)
	if err != nil {
		t.Fatal(err)
	}
	if opts.TLSConfig.MinVersion != tls.VersionTLS12 || opts.TLSConfig.InsecureSkipVerify || *opts.RetryWrites || *opts.RetryReads || opts.Auth.AuthMechanism != "MONGODB-OIDC" || opts.Auth.AuthSource != "$external" {
		t.Fatal("unsafe database transport/auth")
	}
	value, err := opts.Auth.OIDCMachineCallback(context.Background(), &options.OIDCArgs{})
	if err != nil || token.scope != safety.MongoScope || value.ExpiresAt == nil || !value.ExpiresAt.Equal(token.expiry) {
		t.Fatal("OIDC scope/expiry contract")
	}
	token.expiry = time.Now().Add(-time.Hour)
	if _, err := oidcCallback(token)(context.Background(), nil); err == nil {
		t.Fatal("expired token accepted")
	}
	c.AuthMode = "native"
	c.ConnectionString = "mongodb+srv://user:synthetic@" + c.Host + "/?tls=true&retryWrites=false"
	opts, err = configureOptions(options.Client().SetAuth(options.Credential{Username: "user", Password: "synthetic"}), c, nil)
	if err != nil || opts.Auth.AuthMechanism != "SCRAM-SHA-256" || opts.Auth.OIDCMachineCallback != nil || opts.TLSConfig.InsecureSkipVerify {
		t.Fatal("native mode contract")
	}
}
func TestSeedActualDriverIsInsertOnlyIdempotent(t *testing.T) {
	h := hotel()
	d := doc(h)
	responses := []bson.D{cursorResponse(), cursorResponse(), okResponse(1), cursorResponse(d), cursorResponse(), cursorResponse(d), cursorResponse(), okResponse(1), cursorResponse(d), cursorResponse()}
	store, events := mockStore(t, responses...)
	for range 2 {
		if err := store.InsertHotelsWithEmbeddings(context.Background(), []models.HotelForVectorStore{h}); err != nil {
			t.Fatal(err)
		}
	}
	updates := 0
	for _, e := range *events {
		if e.CommandName == "update" {
			updates++
			var command struct {
				Updates []struct {
					Q      bson.M `bson:"q"`
					U      bson.M `bson:"u"`
					Upsert bool   `bson:"upsert"`
				} `bson:"updates"`
			}
			if bson.Unmarshal(e.Command, &command) != nil || len(command.Updates) != 1 {
				t.Fatal("bad update")
			}
			u := command.Updates[0]
			if !u.Upsert || u.Q["_id"] != "agent-go:1" || u.Q["agentOwner"] != Owner || len(u.U) != 1 || u.U["$setOnInsert"] == nil {
				t.Fatal("destructive seed command")
			}
		}
		if e.CommandName == "delete" || e.CommandName == "drop" || e.CommandName == "dropDatabase" {
			t.Fatal("destructive command")
		}
	}
	if updates != 2 {
		t.Fatal("seed not exercised twice")
	}
}
func TestForeignDataAndIndexesRefused(t *testing.T) {
	d := doc(hotel())
	raw, _ := bson.Marshal(d)
	var original bson.M
	_ = bson.Unmarshal(raw, &original)
	for _, change := range []func(bson.M){func(m bson.M) { delete(m, "agentOwner") }, func(m bson.M) { m["HotelName"] = "changed" }, func(m bson.M) { m["foreign"] = "data" }, func(m bson.M) { m["_id"] = "foreign-id" }} {
		m := bson.M{}
		for k, v := range original {
			m[k] = v
		}
		change(m)
		bytes, _ := bson.Marshal(m)
		var bad bson.D
		_ = bson.Unmarshal(bytes, &bad)
		store, events := mockStore(t, cursorResponse(bad))
		if err := store.Cleanup(context.Background()); err == nil {
			t.Fatal("foreign data deleted")
		}
		for _, e := range *events {
			if e.CommandName == "delete" {
				t.Fatal("delete after failed ownership")
			}
		}
	}
	store, _ := mockStore(t, cursorResponse(d), cursorResponse(bson.D{{Key: "name", Value: "customer_index"}, {Key: "key", Value: bson.D{{Key: "HotelName", Value: 1}}}}))
	if _, err := store.InspectOwned(context.Background()); err == nil {
		t.Fatal("foreign index accepted")
	}
}
func TestCleanupOnlyExactOwnedIDsNoDrops(t *testing.T) {
	store, events := mockStore(t, cursorResponse(doc(hotel())), cursorResponse(), cursorResponse(doc(hotel())), okResponse(1), cursorResponse(), cursorResponse())
	if err := store.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	deleted := false
	for _, e := range *events {
		if strings.HasPrefix(e.CommandName, "drop") {
			t.Fatal("cleanup drops resources")
		}
		if e.CommandName == "delete" {
			deleted = true
			var cmd struct {
				Deletes []struct {
					Q bson.M `bson:"q"`
				} `bson:"deletes"`
			}
			_ = bson.Unmarshal(e.Command, &cmd)
			q := cmd.Deletes[0].Q
			if q["agentOwner"] != Owner || q["_id"] == nil || len(q) != 3 || q["$or"] == nil {
				t.Fatal("unscoped deletion")
			}
		}
	}
	if !deleted {
		t.Fatal("cleanup not exercised")
	}
}
func TestActualVectorIndexAndSearchCommands(t *testing.T) {
	d := doc(hotel())
	store, events := mockStore(t, cursorResponse(d), cursorResponse(), okResponse(1), cursorResponse(d), cursorResponse(), cursorResponse(bson.D{{Key: "score", Value: 1.0}, {Key: "hotelID", Value: "agent-go:1"}}), cursorResponse(d))
	if err := store.CreateVectorIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	results, err := store.VectorSearch(context.Background(), []float32{1, 0, 0}, 3)
	if err != nil || len(results) != 1 || results[0].Hotel.HotelName != "Trail House" {
		t.Fatal("search failed", err)
	}
	for _, e := range *events {
		if e.CommandName == "aggregate" {
			text := e.Command.String()
			if !strings.Contains(text, "cosmosSearch") || !strings.Contains(text, "DescriptionVector") || strings.Contains(text, "$where") || strings.Contains(text, "$$ROOT") || !strings.Contains(text, "hotelID") {
				t.Fatal("unsafe search pipeline")
			}
		}
		if e.DatabaseName != store.config.DatabaseName {
			t.Fatal("wrong database")
		}
	}
	for _, algorithm := range []string{"vector-ivf", "vector-hnsw", "vector-diskann"} {
		store.config.Algorithm = algorithm
		raw, _ := bson.MarshalExtJSON(store.indexOptions(), false, false)
		if !strings.Contains(string(raw), algorithm) {
			t.Fatal("wrong algorithm")
		}
	}
}
func TestDatabaseFailuresAndEmptySeedAreErrors(t *testing.T) {
	store, events := mockStore(t)
	if err := store.InsertHotelsWithEmbeddings(context.Background(), nil); err == nil || len(*events) != 0 {
		t.Fatal("empty seed accepted")
	}
	store, _ = mockStore(t, bson.D{{Key: "ok", Value: 0}, {Key: "code", Value: 13}, {Key: "errmsg", Value: "synthetic-sensitive-body"}})
	_, err := store.InspectOwned(context.Background())
	if err == nil || strings.Contains(err.Error(), "synthetic-sensitive") {
		t.Fatal("unsafe database error")
	}
	var cmd mongo.CommandError
	if !errors.As(err, &cmd) {
		t.Fatal("driver cause lost")
	}
}
func TestFileValidation(t *testing.T) {
	for _, data := range []string{"[]", "null", "not json", `[{"HotelId":"1","HotelName":"x","Description":"y"},{"HotelId":"1","HotelName":"x","Description":"y"}]`, `[{"HotelId":"$bad","HotelName":"x","Description":"y"}]`} {
		path := filepath.Join(t.TempDir(), "hotels.json")
		if os.WriteFile(path, []byte(data), 0600) != nil {
			t.Fatal("write")
		}
		if _, err := LoadHotelsFromJSON(path); err == nil {
			t.Fatal("bad file accepted")
		}
	}
}

func TestDigestSurvivesBSONTimeNormalization(t *testing.T) {
	h := hotel()
	h.LastRenovationDate = time.Date(2020, 1, 2, 3, 4, 5, 123456789, time.FixedZone("offset", 7200))
	raw, _ := bson.Marshal(doc(h))
	var m bson.M
	_ = bson.Unmarshal(raw, &m)
	if _, err := decodeOwned(m, 3); err != nil {
		t.Fatal("BSON timestamp roundtrip changed ownership digest", err)
	}
}
func TestCompatibleIndexAcceptedAndMismatchRefused(t *testing.T) {
	c := validConfig()
	sample := &VectorStore{config: c}
	index := bson.D{{Key: "name", Value: IndexName}, {Key: "key", Value: bson.D{{Key: "DescriptionVector", Value: "cosmosSearch"}}}, {Key: "cosmosSearchOptions", Value: sample.indexOptions()}}
	store, _ := mockStore(t, cursorResponse(doc(hotel())), cursorResponse(index))
	if _, err := store.InspectOwned(context.Background()); err != nil {
		t.Fatal(err)
	}
	store, _ = mockStore(t, cursorResponse(doc(hotel())), cursorResponse(index))
	store.config.Algorithm = "vector-hnsw"
	if _, err := store.InspectOwned(context.Background()); err == nil {
		t.Fatal("incompatible index accepted")
	}
}

func TestNestedForeignFieldsRefusedBeforeAnyMutation(t *testing.T) {
	original := doc(hotel())
	raw, _ := bson.Marshal(original)
	decoder := bson.NewDecoder(bson.NewDocumentReader(bytes.NewReader(raw)))
	decoder.DefaultDocumentM()
	var document bson.M
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	address, ok := document["Address"].(bson.M)
	if !ok {
		t.Fatal("address fixture")
	}
	address["CustomerNote"] = "foreign nested edit"
	raw, _ = bson.Marshal(document)
	var foreign bson.D
	if err := bson.Unmarshal(raw, &foreign); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"inspect", "seed", "cleanup", "cleanup-reread"} {
		t.Run(operation, func(t *testing.T) {
			responses := []bson.D{cursorResponse(foreign)}
			if operation == "cleanup-reread" {
				responses = []bson.D{cursorResponse(original), cursorResponse(), cursorResponse(foreign)}
			}
			store, events := mockStore(t, responses...)
			var err error
			switch operation {
			case "inspect":
				_, err = store.InspectOwned(context.Background())
			case "seed":
				err = store.InsertHotelsWithEmbeddings(context.Background(), []models.HotelForVectorStore{hotel()})
			default:
				err = store.Cleanup(context.Background())
			}
			if err == nil {
				t.Fatal("foreign nested field accepted")
			}
			for _, e := range *events {
				if e.CommandName == "delete" || e.CommandName == "update" || e.CommandName == "insert" || strings.HasPrefix(e.CommandName, "drop") {
					t.Fatal("foreign nested edit reached mutation")
				}
			}
		})
	}
}
func TestOwnedNestedBSONOrderIsNotSignificant(t *testing.T) {
	d := doc(hotel())
	for i, field := range d {
		if field.Key == "Address" {
			address := field.Value.(bson.D)
			for l, r := 0, len(address)-1; l < r; l, r = l+1, r-1 {
				address[l], address[r] = address[r], address[l]
			}
			d[i].Value = address
		}
	}
	raw, _ := bson.Marshal(d)
	var document bson.M
	if err := bson.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeOwned(document, 3); err != nil {
		t.Fatal("nested document order caused rejection", err)
	}
}

func TestSearchHydratesStrictOwnedRecordsNotAggregateDocuments(t *testing.T) {
	stored := doc(hotel())
	// Service representation is intentionally not accepted as a stored document.
	// Even if metadata or a different representation reaches the ranking result,
	// only the known ID/score is used; the model receives the independently read row.
	ranking := bson.D{{Key: "score", Value: 0.9}, {Key: "hotelID", Value: "agent-go:1"}, {Key: "document", Value: bson.D{{Key: "HotelName", Value: "Untrusted aggregate representation"}, {Key: "__cosmos_meta__", Value: bson.D{{Key: "score", Value: 0.9}}}}}}
	store, events := mockStore(t, cursorResponse(stored), cursorResponse(), cursorResponse(ranking), cursorResponse(stored))
	results, err := store.VectorSearch(context.Background(), []float32{1, 0, 0}, 3)
	if err != nil || len(results) != 1 || results[0].Hotel.HotelName != "Trail House" || results[0].Score != 0.9 {
		t.Fatal("search did not use strict owned hydration", err)
	}
	finds := 0
	for _, e := range *events {
		if e.CommandName == "find" {
			finds++
			if finds == 2 {
				var cmd struct {
					Filter bson.M `bson:"filter"`
				}
				if bson.Unmarshal(e.Command, &cmd) != nil || cmd.Filter["_id"] != "agent-go:1" || cmd.Filter["agentOwner"] != Owner || len(cmd.Filter) != 2 {
					t.Fatal("hydration was not exact owned ID")
				}
			}
		}
	}
	if finds != 2 {
		t.Fatal("missing preflight or actual stored-record hydration")
	}
}
func TestSearchRejectsForeignHydratedRecordAndInvalidRanking(t *testing.T) {
	stored := doc(hotel())
	for _, rank := range []bson.D{
		{{Key: "score", Value: 1.0}, {Key: "hotelID", Value: "foreign-id"}},
		{{Key: "hotelID", Value: "agent-go:1"}},
		{{Key: "score", Value: math.NaN()}, {Key: "hotelID", Value: "agent-go:1"}},
	} {
		store, events := mockStore(t, cursorResponse(stored), cursorResponse(), cursorResponse(rank))
		if _, err := store.VectorSearch(context.Background(), []float32{1, 0, 0}, 3); err == nil {
			t.Fatal("invalid ranking accepted")
		}
		finds := 0
		for _, e := range *events {
			if e.CommandName == "find" {
				finds++
			}
		}
		if finds != 1 {
			t.Fatal("invalid ranking reached hydration")
		}
	}
	raw, _ := bson.Marshal(stored)
	decoder := bson.NewDecoder(bson.NewDocumentReader(bytes.NewReader(raw)))
	decoder.DefaultDocumentM()
	var foreign bson.M
	if decoder.Decode(&foreign) != nil {
		t.Fatal("fixture")
	}
	foreign["Address"].(bson.M)["CustomerNote"] = "foreign nested mutation"
	raw, _ = bson.Marshal(foreign)
	var modified bson.D
	_ = bson.Unmarshal(raw, &modified)
	rank := bson.D{{Key: "score", Value: 1.0}, {Key: "hotelID", Value: "agent-go:1"}}
	store, _ := mockStore(t, cursorResponse(stored), cursorResponse(), cursorResponse(rank), cursorResponse(modified))
	if _, err := store.VectorSearch(context.Background(), []float32{1, 0, 0}, 3); err == nil {
		t.Fatal("hydration weakened nested foreign-data protection")
	}
	store, _ = mockStore(t, cursorResponse(stored), cursorResponse(), cursorResponse(rank, rank), cursorResponse(stored))
	if _, err := store.VectorSearch(context.Background(), []float32{1, 0, 0}, 3); err == nil {
		t.Fatal("duplicate ranking accepted")
	}
}

func TestStoredCosmosMetadataStillRefused(t *testing.T) {
	stored := append(doc(hotel()), bson.E{Key: "__cosmos_meta__", Value: bson.D{{Key: "score", Value: 0.9}}})
	for _, operation := range []string{"inspect", "cleanup"} {
		t.Run(operation, func(t *testing.T) {
			store, events := mockStore(t, cursorResponse(stored))
			var err error
			if operation == "inspect" {
				_, err = store.InspectOwned(context.Background())
			} else {
				err = store.Cleanup(context.Background())
			}
			if err == nil {
				t.Fatal("server metadata name in stored data bypassed strict ownership")
			}
			for _, e := range *events {
				if e.CommandName == "delete" {
					t.Fatal("stored foreign metadata deleted")
				}
			}
		})
	}
}
