package chunkenc

import (
	"math"
	"testing"
)

func generateUInt64Slice(n int) []uint64 {
	src := make([]uint64, n)
	for i := 0; i < n; i++ {
		src[i] = uint64(math.Sin(float64(i)/360) * 1e5)
	}
	return src
}

func TestUnPackingAll(t *testing.T) {
	n := 100000
	src := generateUInt64Slice(n)
	bitCounts, maxBits := BitStatistics(src)
	bitSelectors := BitSelectors(bitCounts, 16, maxBits)

	b := PackingAll(src, bitSelectors)
	br := newBReader(b.bytes())
	unpacked := UnPackingAll(&br, n)

	for i := range src {
		if src[i] != unpacked[i] {
			t.Fatalf("unpacked value not equal to source value at index %d: got %d, want %d", i, unpacked[i], src[i])
		}
	}
}
