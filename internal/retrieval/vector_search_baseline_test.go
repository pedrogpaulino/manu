package retrieval

import (
	"math"
	"sort"
	"testing"
)

type vectorBaselineCandidate struct {
	ID     string
	Vector []float32
}

type vectorBaselineRanked struct {
	ID       string
	Distance float64
}

func TestExactVectorRecallBaselineAgainstBruteForce(t *testing.T) {
	query := []float32{1, 0, 0}
	candidates := []vectorBaselineCandidate{
		{ID: embeddingTestUUID(701), Vector: []float32{0.9, 0.1, 0}},
		{ID: embeddingTestUUID(702), Vector: []float32{0, 1, 0}},
		{ID: embeddingTestUUID(703), Vector: []float32{1, 0, 0}},
		{ID: embeddingTestUUID(704), Vector: []float32{0.8, 0.2, 0}},
	}
	const k = 2
	expectedIDs := []string{candidates[2].ID, candidates[0].ID}
	bruteForce := bruteForceVectorTopK(query, candidates, k)
	// This is the deterministic result an exact SQL scan must produce for
	// the same corpus. The test intentionally compares IDs, not a floating
	// point aggregate or a claim about production recall.
	if len(bruteForce) != len(expectedIDs) {
		t.Fatalf("brute-force baseline length = %d, want %d", len(bruteForce), len(expectedIDs))
	}
	for index, expectedID := range expectedIDs {
		if bruteForce[index].ID != expectedID {
			t.Fatalf("recall baseline mismatch at %d: got %q want %q", index, bruteForce[index].ID, expectedID)
		}
	}
}

func bruteForceVectorTopK(query []float32, candidates []vectorBaselineCandidate, k int) []vectorBaselineRanked {
	ranked := make([]vectorBaselineRanked, 0, len(candidates))
	for _, candidate := range candidates {
		ranked = append(ranked, vectorBaselineRanked{
			ID:       candidate.ID,
			Distance: bruteForceCosineDistance(query, candidate.Vector),
		})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Distance != ranked[j].Distance {
			return ranked[i].Distance < ranked[j].Distance
		}
		return ranked[i].ID < ranked[j].ID
	})
	if k > len(ranked) {
		k = len(ranked)
	}
	return ranked[:k]
}

func bruteForceCosineDistance(left, right []float32) float64 {
	var dot, leftNorm, rightNorm float64
	for index := range left {
		leftValue := float64(left[index])
		rightValue := float64(right[index])
		dot += leftValue * rightValue
		leftNorm += leftValue * leftValue
		rightNorm += rightValue * rightValue
	}
	return 1 - dot/(math.Sqrt(leftNorm)*math.Sqrt(rightNorm))
}
