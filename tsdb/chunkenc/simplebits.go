package chunkenc

import (
	"math"
	"math/bits"
)

var Simplebits_StepSize = float64(4)
var Simplebits_MaxSegmentsNum = 16
var Simplebits_MaxBits = 64
var Simplebits_MaxN = 16

type BitCounter struct {
	Bits       int
	Proportion float64
	Count      uint64

	TEST_AvgLessAndEqualN float64
	TEST_AvgEqualN        float64
	TEST_AvgGreaterN      float64
}

type BitSelector struct {
	Bits     int
	Selector uint64
	Len      int
	N        int
}

// bitWidth returns the number of bits required to represent v.
func bitWidth(v uint64) int {
	if v == 0 {
		return 1
	}
	return bits.Len64(v)
}

// BitStatistics computes statistics about the bit widths of the values in src.
func BitStatistics(src []uint64) ([]BitCounter, int) {
	bitCounters := make([]BitCounter, Simplebits_MaxBits+1)
	maxBits := 0

	// Count the number of value corresponding to every bit width
	for _, v := range src {
		width := bitWidth(v)
		bitCounters[width].Count++
	}

	for bits := 1; bits <= Simplebits_MaxBits; bits++ {
		if bitCounters[bits].Count > 0 {
			bitCounters[bits].Proportion = float64(bitCounters[bits].Count) / float64(len(src))
			maxBits = bits
		}
		bitCounters[bits].Bits = bits
	}

	// TEST:
	// Calculate average N for data points less than or equal to certain bits width
	for bits := 1; bits <= maxBits; bits++ {
		totalN := uint64(0)
		countN := uint64(0)

		for i := 0; i < len(src); i++ {
			currN := uint64(0)
			for i < len(src) && bitWidth(src[i]) <= bits {
				currN++
				i++
			}
			if currN > 0 {
				totalN += currN
				countN++
			}
		}

		if countN == 0 {
			bitCounters[bits].TEST_AvgLessAndEqualN = -1
		} else {
			bitCounters[bits].TEST_AvgLessAndEqualN = float64(totalN) / float64(countN)
		}
	}

	// TEST:
	// Calculate average N for data points equal to certain bits width
	for bits := 1; bits <= maxBits; bits++ {
		totalN := uint64(0)
		countN := uint64(0)

		for i := 0; i < len(src); i++ {
			currN := uint64(0)
			for i < len(src) && bitWidth(src[i]) == bits {
				currN++
				i++
			}
			if currN > 0 {
				totalN += currN
				countN++
			}
		}

		if countN == 0 {
			bitCounters[bits].TEST_AvgEqualN = -1
		} else {
			bitCounters[bits].TEST_AvgEqualN = float64(totalN) / float64(countN)
		}
	}

	// TEST:
	// Calculate average N for data points greater than certain bits width
	for bits := 1; bits <= maxBits; bits++ {
		totalN := uint64(0)
		countN := uint64(0)

		for i := 0; i < len(src); i++ {
			currN := uint64(0)
			for i < len(src) && bitWidth(src[i]) > bits {
				currN++
				i++
			}
			if currN > 0 {
				totalN += currN
				countN++
			}
		}

		if countN == 0 {
			bitCounters[bits].TEST_AvgGreaterN = -1
		} else {
			bitCounters[bits].TEST_AvgGreaterN = float64(totalN) / float64(countN)
		}
	}

	return bitCounters, maxBits
}

type segments struct {
	cost        float64
	bounds      []int
	proportions []float64
}

// BitSelectors computes the bit selectors for packing the values in src.
func BitSelectors(bitCounters []BitCounter, segmentsNum int, maxBits int) []BitSelector {
	dp := make([][]segments, segmentsNum+1)
	for i := 0; i <= segmentsNum; i++ {
		dp[i] = make([]segments, maxBits+1)
		dp[i][0] = segments{cost: 0, bounds: []int{}, proportions: []float64{}}
	}
	for i := 1; i <= maxBits; i++ {
		dp[0][i] = segments{cost: math.MaxFloat64, bounds: []int{}, proportions: []float64{}}
	}

	for i := 1; i <= segmentsNum; i++ {
		for j := 1; j <= maxBits; j++ {
			minCost := math.MaxFloat64
			currProportion := float64(0)
			min2Bits := -1
			min2Proportion := float64(-1)
			for k := 1; k <= j; k++ {
				currProportion += bitCounters[j-k+1].Proportion
				if dp[i-1][j-k].cost+currProportion*float64(j) < minCost {
					minCost = dp[i-1][j-k].cost + currProportion*float64(j)
					min2Bits = j - k
					min2Proportion = currProportion
				}
			}
			dp[i][j].cost = minCost
			dp[i][j].bounds = append(dp[i-1][min2Bits].bounds, j)
			dp[i][j].proportions = append(dp[i-1][min2Bits].proportions, min2Proportion)
		}
	}

	targetSegmentsNum := -1
	globalMinCost := math.MaxFloat64
	for i := 1; i <= segmentsNum; i++ {
		metaCost := float64(bitWidth(uint64(i-1))) / Simplebits_StepSize
		if dp[i][maxBits].cost+metaCost < globalMinCost {
			globalMinCost = dp[i][maxBits].cost + metaCost
			targetSegmentsNum = i
		}
	}

	bitSelectors := make([]BitSelector, 0)

	for i := 0; i < targetSegmentsNum; i++ {
		tempN := float64(targetSegmentsNum) * Simplebits_StepSize * dp[targetSegmentsNum][maxBits].proportions[i]
		if tempN < 0.5 || i == targetSegmentsNum-1 {
			tempN = 0.5
		} else if tempN >= float64(Simplebits_MaxN)+0.5 {
			tempN = float64(Simplebits_MaxN) - 0.5
		}
		bitSelectors = append(bitSelectors, BitSelector{
			Bits:     dp[targetSegmentsNum][maxBits].bounds[i],
			Selector: uint64(len(bitSelectors)),
			Len:      bitWidth(uint64(targetSegmentsNum - 1)),
			N:        int(tempN + 0.5),
		})
	}

	// Huffman未必能使得编码更优
	// 原因：因为N是按照比例分配的，在一个步长中，这些编码出现的概率与比例无关

	return bitSelectors
}

func bitsPack(src []uint64, selector BitSelector, b *bstream) {
	bits := selector.Bits
	n := selector.N

	b.writeBits(selector.Selector, selector.Len)

	for i := 0; i < n; i++ {
		b.writeBits(src[i], bits)
	}
}

func writeSelectorMeta(selectors []BitSelector, b *bstream) {
	maxSegmentsNumBitWidth := bitWidth(uint64(Simplebits_MaxSegmentsNum - 1))
	maxBitsBitWidth := bitWidth(uint64(Simplebits_MaxBits))
	maxNBitWidth := bitWidth(uint64(Simplebits_MaxN))

	segmentsNum := uint64(len(selectors) - 1)
	b.writeBits(segmentsNum, maxSegmentsNumBitWidth)

	for _, selector := range selectors {
		b.writeBits(uint64(selector.Bits-1), maxBitsBitWidth)
		b.writeBits(uint64(selector.N-1), maxNBitWidth)
	}
}

func readSelectorMeta(br *bstreamReader) []BitSelector {
	maxSegmentsNumBitWidth := bitWidth(uint64(Simplebits_MaxSegmentsNum - 1))
	maxBitsBitWidth := bitWidth(uint64(Simplebits_MaxBits))
	maxNBitWidth := bitWidth(uint64(Simplebits_MaxN))

	segmentsNum, err := br.readBits(uint8(maxSegmentsNumBitWidth))
	if err != nil {
		return nil
	}
	selectors := make([]BitSelector, segmentsNum+1)

	for i := 0; i <= int(segmentsNum); i++ {
		bits, err := br.readBits(uint8(maxBitsBitWidth))
		if err != nil {
			return nil
		}
		n, err := br.readBits(uint8(maxNBitWidth))
		if err != nil {
			return nil
		}
		selectors[i] = BitSelector{
			Bits:     int(bits + 1),
			Selector: uint64(i),
			Len:      bitWidth(segmentsNum),
			N:        int(n + 1),
		}
	}

	return selectors
}

func PackingAll(src []uint64, selectors []BitSelector) *bstream {
	b := &bstream{stream: make([]byte, 0), count: 0}
	i := 0

	writeSelectorMeta(selectors, b)

	for {
		if i >= len(src) {
			break
		}
		remaining := src[i:]

		for _, selector := range selectors {
			if canPack(remaining, selector.N, selector.Bits) {
				bitsPack(remaining, selector, b)
				i += selector.N
				break
			}
		}
	}

	return b
}

func UnPackingAll(br *bstreamReader, num int) []uint64 {
	selectors := readSelectorMeta(br)
	if selectors == nil {
		return nil
	}
	dst := make([]uint64, 0, num)

	i := 0
	for i < num {
		selectorIdx, err := br.readBits(uint8(selectors[0].Len))
		if err != nil {
			return nil
		}
		selector := selectors[selectorIdx]
		for j := 0; j < selector.N; j++ {
			v, err := br.readBits(uint8(selector.Bits))
			if err != nil {
				return nil
			}
			dst = append(dst, v)
			i++
		}
	}

	return dst
}
