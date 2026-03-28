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

func generateInt64Slice(n int) []int64 {
	src := make([]int64, n)
	for i := 0; i < n; i++ {
		src[i] = int64(math.Sin(float64(i)/360) * 1e5)
	}
	return src
}

func TestPackingAll(t *testing.T) {
	n := 100000
	src := generateUInt64Slice(n)
	bitCounts, maxBits := BitStatistics(src)
	bitSelectors := BitSelectors(bitCounts, 16, maxBits, len(src))

	b := PackingAll(src, bitSelectors)
	br := newBReader(b.bytes())
	unpacked := UnPackingAll(&br, n)

	for i := range src {
		if src[i] != unpacked[i] {
			t.Fatalf("unpacked value not equal to source value at index %d: got %d, want %d", i, unpacked[i], src[i])
		}
	}
}

func TestBitPackingAll(t *testing.T) {
	n := 100000
	src := generateUInt64Slice(n)

	b := BitPackingAll(src)
	br := newBReader(b.bytes())
	unpacked := UnBitPackingAll(&br, n)

	if len(unpacked) != len(src) {
		t.Fatalf("unpacked length not equal to source length: got %d, want %d", len(unpacked), len(src))
	}

	for i := range src {
		if src[i] != unpacked[i] {
			t.Fatalf("unpacked value not equal to source value at index %d: got %d, want %d", i, unpacked[i], src[i])
		}
	}
}

func TestVarintPackingAll(t *testing.T) {
	n := 100000
	src := generateUInt64Slice(n)

	b := VarintPackingAll(src)
	br := newBReader(b.bytes())
	unpacked := UnVarintPackingAll(&br, n)

	if len(unpacked) != len(src) {
		t.Fatalf("unpacked length not equal to source length: got %d, want %d", len(unpacked), len(src))
	}

	for i := range src {
		if src[i] != unpacked[i] {
			t.Fatalf("unpacked value not equal to source value at index %d: got %d, want %d", i, unpacked[i], src[i])
		}
	}
}

func TestPForPackingAll(t *testing.T) {
	n := 100000
	src := generateInt64Slice(n)

	b := PForPackingAll(src)
	br := newBReader(b.bytes())
	unpacked := UnPForPackingAll(&br, n)

	if len(unpacked) != len(src) {
		t.Fatalf("unpacked length not equal to source length: got %d, want %d", len(unpacked), len(src))
	}

	for i := range src {
		if src[i] != unpacked[i] {
			t.Fatalf("unpacked value not equal to source value at index %d: got %d, want %d", i, unpacked[i], src[i])
		}
	}
}
