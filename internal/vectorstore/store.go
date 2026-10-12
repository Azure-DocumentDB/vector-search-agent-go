package vectorstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/models"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/safety"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"io"
	"os"
	"reflect"
	"strings"
	"time"
)

type VectorStore struct {
	config     VectorStoreConfig
	client     *mongo.Client
	database   *mongo.Database
	collection *mongo.Collection
}
type record struct {
	ID     string                     `bson:"_id"`
	Owner  string                     `bson:"agentOwner"`
	Digest string                     `bson:"agentDigest"`
	Hotel  models.HotelForVectorStore `bson:",inline"`
}

func digest(h models.HotelForVectorStore) string {
	// BSON timestamps have millisecond precision; canonicalize before hashing.
	h.LastRenovationDate = h.LastRenovationDate.UTC().Truncate(time.Millisecond)
	data, _ := json.Marshal(h)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
func validateHotel(h models.HotelForVectorStore) error {
	if !safety.Identifier(h.HotelID) || strings.TrimSpace(h.HotelName) == "" || len(h.HotelName) > 256 || strings.TrimSpace(h.Description) == "" || len(h.Description) > 8192 || h.LastRenovationDate.Year() < 1 || h.LastRenovationDate.Year() > 9999 {
		return errors.New("invalid hotel ID, name, or description")
	}
	data, err := json.Marshal(h)
	if err != nil || len(data) > 32768 {
		return errors.New("invalid or oversized hotel record")
	}
	return nil
}
func LoadHotelsFromJSON(filePath string) (hotels []models.Hotel, err error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, safety.Protect("data file open", err)
	}
	defer func() {
		if e := file.Close(); e != nil {
			err = errors.Join(err, safety.Protect("data file close", e))
		}
	}()
	data, err := io.ReadAll(io.LimitReader(file, 4*1024*1024+1))
	if err != nil {
		return nil, safety.Protect("data file read", err)
	}
	if len(data) > 4*1024*1024 {
		return nil, errors.New("data file exceeds 4 MiB")
	}
	if err := json.Unmarshal(data, &hotels); err != nil {
		return nil, safety.Protect("data JSON decode", err)
	}
	if len(hotels) == 0 || len(hotels) > 50 {
		return nil, errors.New("data file must contain 1-50 hotels")
	}
	seen := map[string]bool{}
	for _, h := range hotels {
		if err := validateHotel(h.ToVectorStore()); err != nil {
			return nil, err
		}
		if seen[h.HotelID] {
			return nil, errors.New("duplicate hotel ID")
		}
		seen[h.HotelID] = true
	}
	return hotels, nil
}
func closeCursor(cursor *mongo.Cursor) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return cursor.Close(ctx)
}

// InspectOwned is not a multi-tenant authorization boundary: a dedicated disposable
// DB and exclusive use are required. Foreign or modified records/indexes are refused.
func (vs *VectorStore) InspectOwned(ctx context.Context) (ids []string, err error) {
	if err := vs.config.Validate(); err != nil {
		return nil, err
	}
	cursor, err := vs.collection.Find(ctx, bson.D{}, options.Find().SetLimit(51))
	if err != nil {
		return nil, safety.Protect("ownership read", err)
	}
	defer func() {
		if e := closeCursor(cursor); e != nil && err == nil {
			err = safety.Protect("ownership cursor close", e)
		}
	}()
	for cursor.Next(ctx) {
		var raw bson.M
		if e := cursor.Decode(&raw); e != nil {
			return nil, safety.Protect("ownership decode", e)
		}
		r, e := decodeOwned(raw, vs.config.Dimensions)
		if e != nil {
			return nil, e
		}
		ids = append(ids, r.ID)
		if len(ids) > 50 {
			return nil, errors.New("refusing collection larger than the sample limit")
		}
	}
	if e := cursor.Err(); e != nil {
		return nil, safety.Protect("ownership cursor", e)
	}
	if e := vs.inspectIndexes(ctx); e != nil {
		return nil, e
	}
	return ids, nil
}
func decodeOwned(raw bson.M, dimensions int) (record, error) {
	var r record
	encoded, e := bson.Marshal(raw)
	if e != nil {
		return r, safety.Protect("ownership decode", e)
	}
	if e := bson.Unmarshal(encoded, &r); e != nil {
		return r, errors.New("refusing unrecognized data")
	}
	if r.Owner != Owner || r.ID != "agent-go:"+r.Hotel.HotelID || r.Digest != digest(r.Hotel) || validateHotel(r.Hotel) != nil || safety.Vector(r.Hotel.DescriptionVector, dimensions) != nil {
		return r, errors.New("refusing foreign or modified data")
	}
	allowed, _ := bson.Marshal(r)
	var known bson.M
	_ = bson.Unmarshal(allowed, &known)
	if len(raw) != len(known) {
		return r, errors.New("refusing data with foreign fields")
	}
	for key := range raw {
		if _, ok := known[key]; !ok {
			return r, errors.New("refusing data with foreign fields")
		}
	}
	return r, nil
}
func (vs *VectorStore) inspectIndexes(ctx context.Context) (err error) {
	cursor, err := vs.collection.Indexes().List(ctx)
	if err != nil {
		var cmdErr mongo.CommandError
		if errors.As(err, &cmdErr) && cmdErr.Code == 26 {
			return nil
		}
		return safety.Protect("index ownership read", err)
	}
	defer func() {
		if e := closeCursor(cursor); e != nil && err == nil {
			err = safety.Protect("index cursor close", e)
		}
	}()
	for cursor.Next(ctx) {
		var index struct {
			Name   string `bson:"name"`
			Key    bson.M `bson:"key"`
			Search bson.M `bson:"cosmosSearchOptions"`
		}
		if e := cursor.Decode(&index); e != nil {
			return safety.Protect("index decode", e)
		}
		if index.Name == "_id_" {
			continue
		}
		if index.Name != IndexName || len(index.Key) != 1 || index.Key["DescriptionVector"] != "cosmosSearch" {
			return errors.New("refusing foreign index")
		}
		data, _ := bson.Marshal(vs.indexOptions())
		var expected bson.M
		_ = bson.Unmarshal(data, &expected)
		if !reflect.DeepEqual(index.Search, expected) {
			return errors.New("refusing incompatible existing vector index; use a fresh disposable scope")
		}
	}
	if e := cursor.Err(); e != nil {
		return safety.Protect("index cursor", e)
	}
	return nil
}

// Set-on-insert and deterministic IDs never replace records or clear collections.
// On DB failure a safe insert prefix may remain; retries fill IDs without duplicates.
func (vs *VectorStore) InsertHotelsWithEmbeddings(ctx context.Context, hotels []models.HotelForVectorStore) error {
	if err := vs.config.Writes(); err != nil {
		return err
	}
	if len(hotels) < 1 || len(hotels) > 50 {
		return errors.New("seed requires 1-50 embedded hotels")
	}
	seen := map[string]bool{}
	for _, h := range hotels {
		if err := validateHotel(h); err != nil {
			return err
		}
		if err := safety.Vector(h.DescriptionVector, vs.config.Dimensions); err != nil {
			return err
		}
		if seen[h.HotelID] {
			return errors.New("duplicate seed ID")
		}
		seen[h.HotelID] = true
	}
	if _, err := vs.InspectOwned(ctx); err != nil {
		return err
	}
	for _, h := range hotels {
		r := record{ID: "agent-go:" + h.HotelID, Owner: Owner, Digest: digest(h), Hotel: h}
		filter := bson.D{{Key: "_id", Value: r.ID}, {Key: "agentOwner", Value: Owner}}
		if _, err := vs.collection.UpdateOne(ctx, filter, bson.D{{Key: "$setOnInsert", Value: r}}, options.UpdateOne().SetUpsert(true)); err != nil {
			return safety.Protect("idempotent seed (a partial batch may exist)", err)
		}
	}
	_, err := vs.InspectOwned(ctx)
	return err
}
func (vs *VectorStore) indexOptions() bson.D {
	result := bson.D{{Key: "kind", Value: vs.config.Algorithm}, {Key: "dimensions", Value: vs.config.Dimensions}, {Key: "similarity", Value: "COS"}}
	switch vs.config.Algorithm {
	case "vector-ivf":
		result = append(result, bson.E{Key: "numLists", Value: 1})
	case "vector-hnsw":
		result = append(result, bson.E{Key: "m", Value: 16}, bson.E{Key: "efConstruction", Value: 64})
	case "vector-diskann":
		result = append(result, bson.E{Key: "maxDegree", Value: 32}, bson.E{Key: "lBuild", Value: 50})
	}
	return result
}
func (vs *VectorStore) CreateVectorIndex(ctx context.Context) error {
	if err := vs.config.Writes(); err != nil {
		return err
	}
	ids, err := vs.InspectOwned(ctx)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return errors.New("refusing to index an empty collection")
	}
	command := bson.D{{Key: "createIndexes", Value: vs.config.CollectionName}, {Key: "indexes", Value: bson.A{bson.D{{Key: "name", Value: IndexName}, {Key: "key", Value: bson.D{{Key: "DescriptionVector", Value: "cosmosSearch"}}}, {Key: "cosmosSearchOptions", Value: vs.indexOptions()}}}}}
	if err := vs.database.RunCommand(ctx, command).Err(); err != nil {
		return safety.Protect("vector index creation", err)
	}
	return nil
}
func searchPipeline(vector []float32, k int) mongo.Pipeline {
	return mongo.Pipeline{
		{{Key: "$search", Value: bson.D{{Key: "cosmosSearch", Value: bson.D{{Key: "vector", Value: vector}, {Key: "path", Value: "DescriptionVector"}, {Key: "k", Value: k}}}}}},
		{{Key: "$match", Value: bson.D{{Key: "agentOwner", Value: Owner}}}},
		{{Key: "$project", Value: bson.D{{Key: "score", Value: bson.D{{Key: "$meta", Value: "searchScore"}}}, {Key: "document", Value: "$$ROOT"}}}},
		{{Key: "$limit", Value: k}},
	}
}
func (vs *VectorStore) VectorSearch(ctx context.Context, vector []float32, k int) (results []models.HotelSearchResult, err error) {
	if err := safety.Query("vector search", k); err != nil {
		return nil, err
	}
	if err := safety.Vector(vector, vs.config.Dimensions); err != nil {
		return nil, err
	}
	ids, err := vs.InspectOwned(ctx)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, errors.New("sample collection is empty; seed it explicitly first")
	}
	cursor, err := vs.collection.Aggregate(ctx, searchPipeline(vector, k))
	if err != nil {
		return nil, safety.Protect("vector search", err)
	}
	defer func() {
		if e := closeCursor(cursor); e != nil && err == nil {
			err = safety.Protect("search cursor close", e)
		}
	}()
	for cursor.Next(ctx) {
		var row struct {
			Score    float64 `bson:"score"`
			Document bson.M  `bson:"document"`
		}
		if e := cursor.Decode(&row); e != nil {
			return nil, safety.Protect("search decode", e)
		}
		r, e := decodeOwned(row.Document, vs.config.Dimensions)
		if e != nil {
			return nil, e
		}
		results = append(results, models.HotelSearchResult{Hotel: r.Hotel, Score: row.Score})
		if len(results) > k {
			return nil, errors.New("search exceeded result bound")
		}
	}
	if e := cursor.Err(); e != nil {
		return nil, safety.Protect("search cursor", e)
	}
	if len(results) == 0 {
		return nil, errors.New("vector search returned no results")
	}
	return results, nil
}

// Cleanup deletes only a preflighted exact list of tagged sample IDs. Never drop
// databases, collections, or indexes. Concurrent foreign inserts cannot be deleted.
func (vs *VectorStore) Cleanup(ctx context.Context) error {
	if err := vs.config.Cleanup(); err != nil {
		return err
	}
	ids, err := vs.InspectOwned(ctx)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	// Re-read each exact document and use its full snapshot in the delete predicate.
	// A concurrent update no longer matches and causes count mismatch/nonzero.
	snapshots := bson.A{}
	for _, id := range ids {
		var raw bson.M
		if err := vs.collection.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&raw); err != nil {
			return safety.Protect("cleanup snapshot", err)
		}
		if _, err := decodeOwned(raw, vs.config.Dimensions); err != nil {
			return err
		}
		snapshots = append(snapshots, raw)
	}
	filter := bson.D{{Key: "agentOwner", Value: Owner}, {Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}, {Key: "$or", Value: snapshots}}
	result, err := vs.collection.DeleteMany(ctx, filter)
	if err != nil {
		return safety.Protect("scoped cleanup", err)
	}
	if result.DeletedCount != int64(len(ids)) {
		return errors.New("cleanup count mismatch; concurrent mutation suspected")
	}
	remaining, err := vs.InspectOwned(ctx)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return errors.New("cleanup left sample rows; exclusive access required")
	}
	return nil
}
