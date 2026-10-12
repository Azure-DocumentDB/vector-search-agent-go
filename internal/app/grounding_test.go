package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/agents"
	"github.com/Azure-DocumentDB/vector-search-agent-go/internal/models"
)

// The live gate delegates to the genuine store and records only its actual
// returned subset. This wrapper does not supply fixtures or invoke extra models.
type recordingSearchStore struct {
	delegate agents.SearchStore
	names    []string
}

func (s *recordingSearchStore) VectorSearch(ctx context.Context, vector []float32, k int) ([]models.HotelSearchResult, error) {
	results, err := s.delegate.VectorSearch(ctx, vector, k)
	s.names = nil
	if err != nil {
		return nil, err
	}
	for _, result := range results {
		s.names = append(s.names, result.Hotel.HotelName)
	}
	return results, nil
}
func (s *recordingSearchStore) answerMentionsRetrievedHotel(answer string) bool {
	if strings.TrimSpace(answer) == "" {
		return false
	}
	for _, name := range s.names {
		if strings.TrimSpace(name) != "" && strings.Contains(answer, name) {
			return true
		}
	}
	return false
}

type groundingStore struct {
	calls int
	fail  bool
}

func (s *groundingStore) VectorSearch(_ context.Context, vector []float32, k int) ([]models.HotelSearchResult, error) {
	s.calls++
	if s.fail {
		return nil, errors.New("search failed")
	}
	if len(vector) != 3 || vector[0] != 1 || k != 1 {
		return nil, errors.New("delegation changed search parameters")
	}
	return []models.HotelSearchResult{{Hotel: models.HotelForVectorStore{HotelName: "Trail House"}}}, nil
}
func TestLiveGroundingRequiresActualReturnedSubset(t *testing.T) {
	delegate := &groundingStore{}
	recorded := &recordingSearchStore{delegate: delegate}
	results, err := recorded.VectorSearch(context.Background(), []float32{1, 0, 0}, 1)
	if err != nil || len(results) != 1 || delegate.calls != 1 {
		t.Fatal("real delegate contract changed", err)
	}
	if recorded.answerMentionsRetrievedHotel("Choose City Rooms or Beach Lodge.") {
		t.Fatal("accepted seed-only hotel names not returned by search")
	}
	if !recorded.answerMentionsRetrievedHotel("Choose Trail House for its nearby trails.") {
		t.Fatal("actual returned hotel not recognized")
	}
	delegate.fail = true
	if _, err := recorded.VectorSearch(context.Background(), []float32{1, 0, 0}, 1); err == nil || recorded.answerMentionsRetrievedHotel("Trail House") {
		t.Fatal("failed search retained stale grounding names")
	}
}
