package chunkenc

import (
	"math"
	"math/bits"
)

var Simplebits_MaxSegmentsNum = 16
var Simplebits_MaxBits = 64
var Simplebits_MaxN = 16

var BitPacking_BlockSize = 128
var BitPacking_HeaderBitWidth = 6

var PFor_BlockSize = 128

type BitCounter struct {
	Bits       int
	Proportion float64
	Count      uint64
}

// BitWidth returns the number of bits required to represent v.
func BitWidth(v uint64) int {
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
		width := BitWidth(v)
		bitCounters[width].Count++
	}

	for bits := 1; bits <= Simplebits_MaxBits; bits++ {
		if bitCounters[bits].Count > 0 {
			bitCounters[bits].Proportion = float64(bitCounters[bits].Count) / float64(len(src))
			maxBits = bits
		}
		bitCounters[bits].Bits = bits
	}

	return bitCounters, maxBits
}

type BitSelector struct {
	Bits     int
	Selector uint64
	Len      int
	N        int
}

type segments struct {
	cost        float64
	bounds      []int
	proportions []float64
}

// BitSelectors computes the bit selectors for packing the values in src.
func BitSelectors(bitCounters []BitCounter, maxSegmentsNum int, maxBits int, totalNum int) ([]BitSelector, int) {
	if maxBits <= 1 {
		// If all values are 0 or 1, we can use 1 bit to store each value.
		return []BitSelector{{Bits: maxBits, Selector: 0, Len: 1, N: totalNum}}, totalNum / 8
	}

	prefixProportions := make([]float64, maxBits+1)
	for i := 1; i <= maxBits; i++ {
		prefixProportions[i] = prefixProportions[i-1] + bitCounters[i].Proportion
	}
	getProportionSum := func(start, end int) float64 {
		if start > end {
			return 0
		}
		return prefixProportions[end] - prefixProportions[start-1]
	}

	maxSegmentsNum = min(maxSegmentsNum, maxBits)
	dp := make([][]segments, maxSegmentsNum+1)
	for i := 0; i <= maxSegmentsNum; i++ {
		dp[i] = make([]segments, maxBits+1)
	}

	for j := 1; j <= maxBits; j++ {
		proportionSum := getProportionSum(1, j)
		dp[1][j] = segments{
			cost:        proportionSum * float64(j),
			bounds:      []int{j},
			proportions: []float64{proportionSum},
		}
	}

	for i := 2; i <= maxSegmentsNum; i++ {
		dq := make([]int, 0, maxBits)

		for j := i; j <= maxBits; j++ {
			k_new := j - 1
			if k_new >= i-1 {
				for len(dq) > 1 {
					k1, k2 := dq[len(dq)-2], dq[len(dq)-1]
					y1 := dp[i-1][k1].cost
					x1 := prefixProportions[k1]
					y2 := dp[i-1][k2].cost
					x2 := prefixProportions[k2]
					yk_new := dp[i-1][k_new].cost
					xk_new := prefixProportions[k_new]

					if (y2-y1)*(xk_new-x2) >= (yk_new-y2)*(x2-x1) {
						dq = dq[:len(dq)-1]
					} else {
						break
					}
				}
				dq = append(dq, k_new)
			}

			for len(dq) > 1 {
				k1, k2 := dq[0], dq[1]
				y1 := dp[i-1][k1].cost
				x1 := prefixProportions[k1]
				y2 := dp[i-1][k2].cost
				x2 := prefixProportions[k2]
				if y2-y1 <= float64(j)*(x2-x1) {
					dq = dq[1:]
				} else {
					break
				}
			}
			k := dq[0]

			proportionSum := getProportionSum(k+1, j)
			dp[i][j].cost = dp[i-1][k].cost + proportionSum*float64(j)
			dp[i][j].bounds = append(append([]int{}, dp[i-1][k].bounds...), j)
			dp[i][j].proportions = append(append([]float64{}, dp[i-1][k].proportions...), proportionSum)
		}
	}

	targetSegmentsNum := -1
	globalMinCost := math.MaxFloat64
	for segNum := 1; segNum <= maxSegmentsNum; segNum++ {
		if len(dp[segNum][maxBits].bounds) == 0 {
			continue
		}
		currCost := dp[segNum][maxBits].cost
		metaCost := BitWidth(uint64(segNum - 1))
		for i := 0; i < segNum; i++ {
			N := float64(1)
			if i < segNum-1 {
				N = min(max(1/(1-dp[segNum][maxBits].proportions[i])-1, 1), float64(Simplebits_MaxN))
			}
			currCost += dp[segNum][maxBits].proportions[i] * float64(metaCost) / N
		}
		if currCost < globalMinCost {
			globalMinCost = currCost
			targetSegmentsNum = segNum
		}
	}

	if targetSegmentsNum == -1 {
		return []BitSelector{{Bits: maxBits, Selector: 0, Len: 1, N: 1}}, math.MaxInt
	}

	bitSelectors := make([]BitSelector, 0)
	for i := 0; i < targetSegmentsNum; i++ {
		N := float64(1)
		if i < targetSegmentsNum-1 {
			N = min(max(1/(1-dp[targetSegmentsNum][maxBits].proportions[i])-1, 1), float64(Simplebits_MaxN))
		}
		bitSelectors = append(bitSelectors, BitSelector{
			Bits:     dp[targetSegmentsNum][maxBits].bounds[i],
			Selector: uint64(len(bitSelectors)),
			Len:      BitWidth(uint64(targetSegmentsNum - 1)),
			N:        int(N),
		})
	}

	return bitSelectors, int(globalMinCost * float64(totalNum) / 8)
}

// BitSelectors computes the bit selectors for packing the values in src.
func BitSelectors2(bitCounters []BitCounter, maxSegmentsNum int, maxBits int, totalNum int) []BitSelector {
	if maxBits == 1 {
		return []BitSelector{
			{
				Bits:     1,
				Selector: 0,
				Len:      1,
				N:        totalNum,
			},
		}
	}
	maxSegmentsNum = min(maxSegmentsNum, maxBits)
	dp := make([][]segments, maxSegmentsNum+1)
	for i := 0; i <= maxSegmentsNum; i++ {
		dp[i] = make([]segments, maxBits+1)
		dp[i][0] = segments{cost: 0, bounds: []int{}, proportions: []float64{}}
	}
	for i := 1; i <= maxBits; i++ {
		dp[0][i] = segments{cost: math.MaxFloat64, bounds: []int{}, proportions: []float64{}}
	}

	for i := 1; i <= maxSegmentsNum; i++ {
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
	for segNum := 1; segNum <= maxSegmentsNum; segNum++ {
		currProportion := float64(0)
		currCost := dp[segNum][maxBits].cost
		metaCost := BitWidth(uint64(segNum - 1))
		for i := 0; i < segNum; i++ {
			N := float64(1)
			if i < segNum-1 {
				currProportion += dp[segNum][maxBits].proportions[i]
				N = min(max(1/(1-currProportion)-1, 1), float64(Simplebits_MaxN))
			}
			currCost += dp[segNum][maxBits].proportions[i] * float64(metaCost) / N
		}
		if currCost < globalMinCost {
			globalMinCost = currCost
			targetSegmentsNum = segNum
		}
	}

	bitSelectors := make([]BitSelector, 0)
	currProprtion := float64(0)
	for i := 0; i < targetSegmentsNum; i++ {
		N := float64(1)
		if i < targetSegmentsNum-1 {
			currProprtion += dp[targetSegmentsNum][maxBits].proportions[i]
			N = min(max(1/(1-currProprtion)-1, 1), float64(Simplebits_MaxN))
		}
		bitSelectors = append(bitSelectors, BitSelector{
			Bits:     dp[targetSegmentsNum][maxBits].bounds[i],
			Selector: uint64(len(bitSelectors)),
			Len:      BitWidth(uint64(targetSegmentsNum - 1)),
			N:        int(N),
		})
	}

	return bitSelectors
}

func bitsPack3(src []uint64, selector BitSelector, b *bstream) {
	bits := selector.Bits
	n := selector.N

	b.writeBits(selector.Selector, selector.Len)

	for i := 0; i < n; i++ {
		b.writeBits(src[i], bits)
	}
}

func bitsPack2(src []uint64, selector BitSelector, b *bstream) {
	bits := selector.Bits
	n := selector.N

	b.writeBits(selector.Selector, selector.Len)

	var word uint64
	var bitpos uint = 0

	for i := 0; i < n; i++ {
		if bitpos+uint(bits) > 64 {
			b.writeBits(word, int(bitpos))
			word = 0
			bitpos = 0
		}

		word |= src[i] << bitpos
		bitpos += uint(bits)
	}

	if bitpos > 0 {
		b.writeBits(word, int(bitpos))
	}
}

func bitsPack(src []uint64, selector BitSelector, b *bstream) {
	bits := selector.Bits
	n := selector.N

	b.writeBits(selector.Selector, selector.Len)

	for i := 0; i < n; i++ {
		val := src[i]
		nbits := bits

		if b.count == 0 {
			b.stream = append(b.stream, 0)
			b.count = 8
		}

		for nbits > 0 {
			lastByteIndex := len(b.stream) - 1
			write := min(int(b.count), nbits)

			shift := 64 - nbits
			toWrite := (val << shift) >> (64 - write)

			b.stream[lastByteIndex] |= byte(toWrite << (b.count - uint8(write)))

			b.count -= uint8(write)
			nbits -= write

			if b.count == 0 && nbits > 0 {
				b.stream = append(b.stream, 0)
				b.count = 8
			}
		}
	}
}

func writeSelectorMeta(selectors []BitSelector, b *bstream) {
	maxSegmentsNumBitWidth := BitWidth(uint64(Simplebits_MaxSegmentsNum - 1))
	maxBitsBitWidth := BitWidth(uint64(Simplebits_MaxBits))
	maxNBitWidth := BitWidth(uint64(Simplebits_MaxN))

	segmentsNum := uint64(len(selectors) - 1)
	b.writeBits(segmentsNum, maxSegmentsNumBitWidth)

	for _, selector := range selectors {
		b.writeBits(uint64(selector.Bits-1), maxBitsBitWidth)
		b.writeBits(uint64(selector.N-1), maxNBitWidth)
	}
}

func readSelectorMeta(br *bstreamReader) []BitSelector {
	maxSegmentsNumBitWidth := BitWidth(uint64(Simplebits_MaxSegmentsNum - 1))
	maxBitsBitWidth := BitWidth(uint64(Simplebits_MaxBits))
	maxNBitWidth := BitWidth(uint64(Simplebits_MaxN))

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
			Len:      BitWidth(segmentsNum),
			N:        int(n + 1),
		}
	}

	return selectors
}

func PackingAll(src []uint64, selectors []BitSelector) *bstream {
	b := &bstream{stream: make([]byte, 0, 4096), count: 0}
	// meta_total_bytes := 0

	i := 0

	writeSelectorMeta(selectors, b)
	// meta_total_bytes = len(b.stream) * 8

	for {
		if i >= len(src) {
			break
		}
		remaining := src[i:]

		for _, selector := range selectors {
			if canPack(remaining, selector.N, selector.Bits) {
				bitsPack(remaining, selector, b)
				i += selector.N

				// selector.Len bits for metadata
				// meta_total_bytes += selector.Len
				break
			}
		}
	}

	// meta_total_bytes is in bits, convert it to bytes
	// meta_total_bytes /= 8

	return b
}

// Assuming the number of data points has already been stored in CLIterator.
// There is no need to redundantly store it in Simplebits metadata.
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

// Leave boundary management to CLIterator, here is no numRead or numTotal.
type SimplebitsDecoder struct {
	br           bstreamReader
	selectors    []BitSelector
	currSelector BitSelector
	currIdx      int
}

func NewSimplebitsDecoder(data []byte) *SimplebitsDecoder {
	decoder := &SimplebitsDecoder{
		br:      newBReader(data),
		currIdx: -1,
	}

	decoder.selectors = readSelectorMeta(&decoder.br)
	if decoder.selectors == nil {
		return nil
	}

	return decoder
}

func (d *SimplebitsDecoder) Next() error {
	if d.currIdx == -1 || d.currIdx+1 >= d.currSelector.N {
		selectorIdx, err := d.br.readBits(uint8(d.selectors[0].Len))
		if err != nil {
			return err
		}
		d.currSelector = d.selectors[selectorIdx]
		d.currIdx = 0
	} else {
		d.currIdx++
	}

	return nil
}

func (d *SimplebitsDecoder) Read() uint64 {
	v, _ := d.br.readBits(uint8(d.currSelector.Bits))
	return v
}

func BitPackingAll(src []uint64) *bstream {
	b := &bstream{stream: make([]byte, 0), count: 0}
	// meta_total_bytes := 0

	for i := 0; i < len(src); i += BitPacking_BlockSize {
		end := i + BitPacking_BlockSize
		if end > len(src) {
			end = len(src)
		}
		block := src[i:end]

		maxBits := 0
		for _, v := range block {
			width := BitWidth(v)
			if width > maxBits {
				maxBits = width
			}
		}

		b.writeBits(uint64(maxBits-1), BitPacking_HeaderBitWidth)
		for _, v := range block {
			b.writeBits(v, maxBits)
		}

		// each block store BitPacking_HeaderBitWidth bits for metadata
		// meta_total_bytes += BitPacking_HeaderBitWidth
	}

	// meta_total_bytes is in bits, convert it to bytes
	// meta_total_bytes /= 8

	return b
}

func UnBitPackingAll(br *bstreamReader, num int) []uint64 {
	dst := make([]uint64, 0, num)

	i := 0
	for i < num {
		// Read the number of bits used for this block.
		maxBits, err := br.readBits(uint8(BitPacking_HeaderBitWidth))
		if err != nil {
			// Returning nil or an error might be appropriate.
			// For now, returning what we have.
			return dst
		}
		// maxBits is stored as maxBits-1, so we need to add 1 back.
		maxBits += 1

		// Determine how many values are in this block.
		remaining := num - i
		numInBlock := min(BitPacking_BlockSize, remaining)

		// Read the values.
		for j := 0; j < numInBlock; j++ {
			v, err := br.readBits(uint8(maxBits))
			if err != nil {
				return dst
			}
			dst = append(dst, v)
		}
		i += numInBlock
	}

	return dst
}

// BitPackingDecoder is a streaming decoder for the BitPacking algorithm.
type BitPackingDecoder struct {
	br         bstreamReader
	numTotal   int // Total number of values to be read.
	numRead    int // Number of values read so far.
	maxBits    int // The bit width for the current block.
	numInBlock int // Number of values in the current block.
	idxInBlock int // Index of the next value to read in the current block.
}

// NewBitPackingDecoder creates a new BitPackingDecoder.
// It requires the compressed data and the total number of values to expect.
func NewBitPackingDecoder(data []byte, numTotal int) *BitPackingDecoder {
	return &BitPackingDecoder{
		br:       newBReader(data),
		numTotal: numTotal,
		// Initialize idxInBlock to be equal to numInBlock to trigger reading
		// the first block header in the first call to Next().
		idxInBlock: 0,
		numInBlock: 0,
	}
}

// Next prepares the next value for reading.
// It returns false if there are no more values to read or an error occurs.
func (d *BitPackingDecoder) Next() bool {
	// Check if we have read all values.
	if d.numRead >= d.numTotal {
		return false
	}

	// Check if we need to read the header of a new block.
	if d.idxInBlock >= d.numInBlock {
		// Read the number of bits used for this block.
		maxBits, err := d.br.readBits(uint8(BitPacking_HeaderBitWidth))
		if err != nil {
			return false
		}
		// maxBits is stored as maxBits-1, so we need to add 1 back.
		d.maxBits = int(maxBits + 1)

		// Determine how many values are in this block.
		remaining := d.numTotal - d.numRead
		d.numInBlock = min(BitPacking_BlockSize, remaining)
		d.idxInBlock = 0
	}

	d.idxInBlock++
	d.numRead++

	return true
}

// Read returns the current value.
func (d *BitPackingDecoder) Read() uint64 {
	// It's assumed Next() has been called and returned true.
	v, err := d.br.readBits(uint8(d.maxBits))
	if err != nil {
		// This should ideally not happen if Next() returned true.
		// Returning 0 is a safe default.
		return 0
	}
	return v
}

// writeVarint writes a uint64 to the bstream using variable-length encoding.
func writeVarint(b *bstream, v uint64) {
	for v >= 0x80 {
		b.writeBits((v&0x7F)|0x80, 8)
		v >>= 7
	}
	b.writeBits(v, 8)
}

// readVarint reads a uint64 from the bstreamReader using variable-length encoding.
func readVarint(br *bstreamReader) (uint64, error) {
	var v uint64
	var shift uint
	for {
		b, err := br.readBits(8)
		if err != nil {
			return 0, err
		}
		v |= (b & 0x7F) << shift
		if (b & 0x80) == 0 {
			break
		}
		shift += 7
	}
	return v, nil
}

func VarintPackingAll(src []uint64) *bstream {
	b := &bstream{stream: make([]byte, 0), count: 0}

	for _, v := range src {
		writeVarint(b, v)
	}

	// each byte store 1 bit for metadata
	// meta_total_bytes := len(b.stream) / 8

	return b
}

func UnVarintPackingAll(br *bstreamReader, num int) []uint64 {
	dst := make([]uint64, 0, num)

	for i := 0; i < num; i++ {
		v, err := readVarint(br)
		if err != nil {
			return dst
		}
		dst = append(dst, v)
	}

	return dst
}

func PForPackingAll(src []int64) *bstream {
	b := &bstream{stream: make([]byte, 0), count: 0}
	// meta_total_bytes := 0

	for i := 0; i < len(src); i += PFor_BlockSize {
		end := i + PFor_BlockSize
		if end > len(src) {
			end = len(src)
		}
		block := src[i:end]

		// Find the minimum value in the block.
		minVal := block[0]
		minValZ := uint64(0)
		for _, v := range block {
			if v < minVal {
				minVal = v
			}
		}
		if minVal < 0 {
			minValZ = uint64(-minVal*2 - 1)
		} else {
			minValZ = uint64(minVal * 2)
		}
		writeVarint(b, minValZ)

		// Calculate the deltas and find the maximum delta.
		maxBits := 0
		deltas := make([]uint64, len(block))
		for j, v := range block {
			deltas[j] = uint64(v - minVal)
			bw := BitWidth(deltas[j])
			if bw > maxBits {
				maxBits = bw
			}
		}

		// Find optimal bit width by calculating the cost for each possible bit width.
		bestBits := 0
		minCost := math.MaxInt64

		for bits := 1; bits <= maxBits; bits++ {
			exceptionsCount := 0
			for _, delta := range deltas {
				if BitWidth(delta) > bits {
					exceptionsCount++
				}
			}
			// Cost function: bits for regular values + bits for exceptions + overhead for exceptions
			cost := (len(block)-exceptionsCount)*bits + exceptionsCount*maxBits
			if cost <= minCost {
				minCost = cost
				bestBits = bits
			}
		}

		exceptionsIndex := make([]int, 0)
		for j, delta := range deltas {
			if BitWidth(delta) > bestBits {
				exceptionsIndex = append(exceptionsIndex, j)
			}
		}

		b.writeBits(uint64(bestBits-1), 6) // bestBits can be at most Simplebits_MaxBits(64)
		b.writeBits(uint64(maxBits-1), 6)  // maxBits can be at most Simplebits_MaxBits(64)

		// exceptions count can be at most PFor_BlockSize(127)
		b.writeBits(uint64(len(exceptionsIndex)), 7)

		// Write exceptions index and values
		for _, index := range exceptionsIndex {
			// index can be at most PFor_BlockSize(128)
			b.writeBits(uint64(index), 7)
		}

		for _, index := range exceptionsIndex {
			b.writeBits(deltas[index], maxBits)
		}

		// Write regular values
		for _, delta := range deltas {
			if BitWidth(delta) <= bestBits {
				b.writeBits(delta, bestBits)
			}
		}

		// bestBits, maxBits, exceptions count, exceptions index and values are metadata
		// meta_total_bytes += (6 + 6 + 7 + len(exceptionsIndex)*7)
	}

	// meta_total_bytes is in bits, convert it to bytes
	// meta_total_bytes /= 8

	return b
}

func UnPForPackingAll(br *bstreamReader, num int) []int64 {
	dst := make([]int64, 0, num)
	i := 0

	for i < num {
		// Read minVal
		minValZ, err := readVarint(br)
		minVal := int64(0)
		if err != nil {
			return nil
		}
		if minValZ%2 == 0 {
			minVal = int64(minValZ / 2)
		} else {
			minVal = int64(-(minValZ/2 + 1))
		}

		// Read metadata
		// bestBits can be at most Simplebits_MaxBits(64)
		bestBitsMinus1, err := br.readBits(6)
		if err != nil {
			return nil
		}
		bestBits := int(bestBitsMinus1 + 1)

		// maxBits can be at most Simplebits_MaxBits(64)
		maxBitsMinus1, err := br.readBits(6)
		if err != nil {
			return nil
		}
		maxBits := int(maxBitsMinus1 + 1)

		// maxIndexDelta can be at most PFor_BlockSize(128)
		indexBitsMinus1, err := br.readBits(7)
		if err != nil {
			return nil
		}
		indexBits := int(indexBitsMinus1 + 1)

		indexBits = 7

		// exceptions count can be at most PFor_BlockSize(127)
		exceptionsCount, err := br.readBits(7)
		if err != nil {
			return nil
		}

		// Read exceptions
		exceptions := make(map[int]uint64, exceptionsCount)
		exceptionIndices := make([]int, exceptionsCount)

		currIndex := 0
		for j := 0; j < int(exceptionsCount); j++ {
			idx, err := br.readBits(uint8(indexBits))
			if err != nil {
				return nil
			}
			currIndex += int(idx)
			exceptionIndices[j] = currIndex
		}
		for _, idx := range exceptionIndices {
			val, err := br.readBits(uint8(maxBits))
			if err != nil {
				return nil
			}
			exceptions[idx] = val
		}

		// Read regular values and reconstruct the block
		numInBlock := PFor_BlockSize
		if num-i < PFor_BlockSize {
			numInBlock = num - i
		}

		for j := 0; j < numInBlock; j++ {
			if val, isException := exceptions[j]; isException {
				dst = append(dst, minVal+int64(val))
			} else {
				delta, err := br.readBits(uint8(bestBits))
				if err != nil {
					return nil
				}
				dst = append(dst, minVal+int64(delta))
			}
		}
		i += numInBlock
	}

	return dst
}
