package retrieval

import (
	"fmt"
	"testing"
)

// BenchmarkExactVectorSearchBruteForce is a reproducible CPU baseline for
// the initial exact corpus. It is intentionally separate from SQL/pgvector
// integration and makes no latency or recall claim when it is not run.
func BenchmarkExactVectorSearchBruteForce(b *testing.B) {
	const (
		dimension = 32
		corpus    = 1024
	)
	query := make([]float32, dimension)
	query[0] = 1
	candidates := make([]vectorBaselineCandidate, corpus)
	for index := range candidates {
		vector := make([]float32, dimension)
		vector[index%dimension] = 1
		vector[(index+1)%dimension] = float32(index%7) / 100
		candidates[index] = vectorBaselineCandidate{
			ID:     fmt.Sprintf("%036d", index+1),
			Vector: vector,
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = bruteForceVectorTopK(query, candidates, 20)
	}
}
