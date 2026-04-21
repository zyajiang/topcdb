package chunkenc

import (
	"encoding/binary"
	"math"
	"sort"

	"github.com/prometheus/prometheus/model/histogram"
)

type CompressType uint8

const (
	Simplebits CompressType = iota
	Auto
	BitPacking
	Varint
	PFor
	QSimple8b
	Huffman
	SZ3
	Machete
	MOST
	None
)

var DefaultCtype CompressType = Huffman
var DefaultErrbound float64 = 1e-2

// SZ3 can not work on too small chunks.
var SZ3_MinChunkSize int = 1000

// 2-phase compactable lossy compression chunk.
type CLChunk struct {
	b bstream
}

func NewCLChunk() *CLChunk {
	b := make([]byte, 12)
	return &CLChunk{b: bstream{stream: b, count: 0}}
}

func (c *CLChunk) Reset(stream []byte) {
	c.b.Reset(stream)
}

func (c *CLChunk) Encoding() Encoding {
	return EncCL
}

func (c *CLChunk) Bytes() []byte {
	return c.b.bytes()
}

func (c *CLChunk) NumSamples() int {
	return int(binary.BigEndian.Uint16(c.Bytes()))
}

func (c *CLChunk) Compact() {
	if l := len(c.b.stream); cap(c.b.stream) > l+chunkCompactCapacityThreshold {
		buf := make([]byte, l)
		copy(buf, c.b.stream)
		c.b.stream = buf
	}
}

func (c *CLChunk) Appender() (Appender, error) {
	a := &CLAppender{
		b: &c.b,
		timestamps: &TimestampsDoD{
			b:   &bstream{stream: make([]byte, 0, 32), count: 0},
			num: 0,
		},

		v:   0,
		num: 0,

		ctype:    DefaultCtype,
		errbound: DefaultErrbound,

		QSimple8b_encoder: NewSimple8bEncoder(),
		Integer_buffer:    make([]int64, 0),
		IntDelta_buffer:   make([]uint64, 0),
		Float_buffer:      make([]float64, 0),
	}
	return a, nil
}

func (c *CLChunk) iterator(it Iterator) *CLIterator {
	if CLIter, ok := it.(*CLIterator); ok {
		if !CLIter.Reset(c.b.bytes()) {
			return nil
		}
		return CLIter
	}
	var CLIter CLIterator
	if !CLIter.Reset(c.b.bytes()) {
		return nil
	}
	return &CLIter
}

func (c *CLChunk) Iterator(it Iterator) Iterator {
	return c.iterator(it)
}

type CLAppender struct {
	b          *bstream
	timestamps *TimestampsDoD

	v   int64
	num uint16

	ctype    CompressType
	errbound float64

	QSimple8b_encoder *Simple8bEncoder

	Integer_buffer  []int64
	IntDelta_buffer []uint64
	Float_buffer    []float64

	TEST_timestampTotalSize int
	TEST_floatTotalSize     int

	// Statistics for choosing the best bit-packing scheme when CompressType is Simplebits.
	maxBits      int
	bitCounters  []BitCounter
	bitSelectors []BitSelector
}

func (a *CLAppender) SetErrorBound(errbound float64) {
	a.errbound = errbound
}

func (a *CLAppender) SetCompressType(ctype CompressType) {
	a.ctype = ctype
}

func (a *CLAppender) Append(t int64, v float64) {
	// Write the timestamp.
	a.timestamps.Append(t)
	a.num = binary.BigEndian.Uint16(a.b.bytes()) + 1
	binary.BigEndian.PutUint16(a.b.bytes(), a.num)

	// When compressType is Machete, MOST or SZ3, we do not need to quantize the float value.
	if a.ctype == Machete || a.ctype == MOST || a.ctype == SZ3 {
		a.Float_buffer = append(a.Float_buffer, v)
		return
	}

	// Step1: Quantize the float value.
	var f2i int64
	if v >= 0 {
		f2i = int64(v/(2*a.errbound) + 0.5)
	} else {
		f2i = int64(v/(2*a.errbound) - 0.5)
	}

	// Step2: Predict the current value from the previous value.
	// Calculate the delta between the quantized value and the previous one.
	fdelta := f2i - a.v
	a.v = f2i

	if a.ctype == PFor {
		a.Integer_buffer = append(a.Integer_buffer, fdelta)
		return
	}

	// Zigzag encode the delta.
	if fdelta >= 0 {
		fdelta <<= 1
	} else {
		fdelta = -2*fdelta - 1
	}

	// Step3: Bit-packing encode the delta.
	if a.ctype == QSimple8b {
		a.QSimple8b_encoder.Write(uint64(fdelta))
	} else if a.ctype == Auto || a.ctype == Simplebits || a.ctype == BitPacking || a.ctype == Varint || a.ctype == Huffman {
		a.IntDelta_buffer = append(a.IntDelta_buffer, uint64(fdelta))
	}
}

// AppendQuantizer appends the quantized integer value directly, which happens when compaction.
func (a *CLAppender) AppendQuantizer(t int64, v int64) {
	// Write the timestamp.
	a.timestamps.Append(t)
	a.num = binary.BigEndian.Uint16(a.b.bytes()) + 1
	binary.BigEndian.PutUint16(a.b.bytes(), a.num)

	// Step1: Quantize the float value.
	// Step2: Predict the current value from the previous value.
	// Calculate the delta between the quantized value and the previous one.
	fdelta := v - a.v
	a.v = v

	if a.ctype == PFor {
		a.Integer_buffer = append(a.Integer_buffer, fdelta)
		return
	}

	// Zigzag encode the delta.
	if fdelta >= 0 {
		fdelta <<= 1
	} else {
		fdelta = -2*fdelta - 1
	}

	// Step3: Bit-packing encode the delta.
	if a.ctype == QSimple8b {
		a.QSimple8b_encoder.Write(uint64(fdelta))
	} else if a.ctype == Auto || a.ctype == Simplebits || a.ctype == BitPacking || a.ctype == Varint || a.ctype == Huffman {
		a.IntDelta_buffer = append(a.IntDelta_buffer, uint64(fdelta))
	}
}

func (a *CLAppender) NumSamples() int {
	return int(a.num)
}

func (a *CLAppender) TimestampSize() int {
	return a.TEST_timestampTotalSize
}

func (a *CLAppender) FloatSize() int {
	return a.TEST_floatTotalSize
}

func (a *CLAppender) estimateHuffmanSize() int {
	if len(a.IntDelta_buffer) == 0 {
		return math.MaxInt
	}

	freqMap := make(map[uint64]int)
	for _, v := range a.IntDelta_buffer {
		freqMap[v]++
	}

	totalNum := float64(len(a.IntDelta_buffer))
	var entropy float64

	for _, count := range freqMap {
		if count > 0 {
			p := float64(count) / totalNum
			entropy -= float64(count) * math.Log2(p)
		}
	}

	keys := make([]uint64, 0)
	for key := range freqMap {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	// Metadata cost.
	lastKey := uint64(0)
	lastFreq := 0

	// Simple8b metadata cost is 4bits / 64bits for both keys and freqs.
	max1, max2 := 0, 0
	for _, key := range keys {
		max1 = max(max1, BitWidth(key-lastKey))
		if freqMap[key] > lastFreq {
			max2 = max(max2, BitWidth(uint64(freqMap[key]-lastFreq)))
		} else {
			max2 = max(max2, BitWidth(uint64(lastFreq-freqMap[key])))
		}

		lastKey = key
		lastFreq = freqMap[key]
	}
	metaCost := (max1 + max2 + 1) * len(keys) / 8

	return int(math.Ceil(entropy/8.0)) + metaCost
}

func (a *CLAppender) estimateSimplebitsSize() int {
	var cost_bytes int
	a.bitSelectors, cost_bytes = BitSelectors(a.bitCounters, Simplebits_MaxSegmentsNum, a.maxBits, len(a.IntDelta_buffer))
	return cost_bytes
}

func (a *CLAppender) EstimatedSize() int {
	switch a.ctype {
	case SZ3, Machete, MOST:
		return len(a.timestamps.b.stream) + len(a.Float_buffer)*1
	case QSimple8b:
		return len(a.timestamps.b.stream) + a.QSimple8b_encoder.EstimatedSize()
	default:
		return len(a.timestamps.b.stream) + int(a.num)
	}
}

// Compact the chunk. Flush the remaining data to the stream.
func (a *CLAppender) Compact() error {
	var compressed_data []byte
	var err error

	ptr := len(a.timestamps.b.stream) + 12
	if a.timestamps.b.count == 8 {
		ptr -= 1
	}

	if a.ctype == QSimple8b {
		compressed_data, err = a.QSimple8b_encoder.Bytes()
		if err != nil {
			return err
		}
	} else if a.ctype == Auto {
		if a.bitCounters == nil {
			a.bitCounters, a.maxBits = BitStatistics(a.IntDelta_buffer)
		}

		p8bits := float64(0)
		for i := 1; i <= 8; i++ {
			p8bits += a.bitCounters[i].Proportion
		}
		p12bits := p8bits
		for i := 9; i <= 12; i++ {
			p12bits += a.bitCounters[i].Proportion
		}

		// When the number of values that can be encoded in 8 bits is less than 80%
		// or the number of values that need more than 12 bits is more than 5%,
		// switch to Auto to choose a better compression method.
		if p8bits > 0.99 {
			a.ctype = Huffman
		} else if p8bits > 0.8 && p12bits > 0.95 {
			a.ctype = QSimple8b

			compressed_data, err = EncodeAll(a.IntDelta_buffer)
			if err != nil {
				return err
			}
		} else {
			huffman_cost := a.estimateHuffmanSize()
			simplebits_cost := a.estimateSimplebitsSize()

			if huffman_cost < simplebits_cost {
				a.ctype = Huffman
			} else {
				a.ctype = Simplebits
			}
		}
	}

	switch a.ctype {
	case Huffman:
		b := HuffmanEncodeWithoutTimesMap(&a.IntDelta_buffer, int(ptr))
		copy(b.stream[12:ptr], a.timestamps.b.stream[0:ptr-12])
		a.b.stream = b.stream
		a.b.count = b.count
		writeCLMeta(a.num, uint32(ptr), a.errbound, a.ctype, a.b.stream)
		a.TEST_floatTotalSize = len(b.stream) - ptr
		return nil
	case Simplebits:
		if a.bitCounters == nil {
			a.bitCounters, a.maxBits = BitStatistics(a.IntDelta_buffer)
		}
		if a.bitSelectors == nil {
			a.bitSelectors, _ = BitSelectors(a.bitCounters, Simplebits_MaxSegmentsNum, a.maxBits, len(a.IntDelta_buffer))
		}
		compressed_data = PackingAll(a.IntDelta_buffer, a.bitSelectors).bytes()
	case BitPacking:
		compressed_data = BitPackingAll(a.IntDelta_buffer).bytes()
	case Varint:
		compressed_data = VarintPackingAll(a.IntDelta_buffer).bytes()
	case PFor:
		compressed_data = PForPackingAll(a.Integer_buffer).bytes()
	case SZ3:
		var outSize uint64
		if a.num < uint16(SZ3_MinChunkSize) {
			compressed_data = SZ_Compress(1, a.Float_buffer, &outSize, 0, a.errbound, 0, 0, 0, 0, 0, 0, uint64(SZ3_MinChunkSize))
		} else {
			compressed_data = SZ_Compress(1, a.Float_buffer, &outSize, 0, a.errbound, 0, 0, 0, 0, 0, 0, uint64(a.num))
		}
	case MOST:
		compressed_data = MOST_Compress(a.Float_buffer, a.errbound, 5)
	case Machete:
		compressed_data = Machete_Compress(a.Float_buffer, int64(len(a.Float_buffer)), a.errbound)
	}

	// a.TEST_timestampTotalSize = len(a.timestamps.b.stream)
	a.TEST_floatTotalSize = len(compressed_data)

	totalBytes := ptr + len(compressed_data)
	if totalBytes > len(a.b.stream) {
		buf := make([]byte, totalBytes)
		copy(buf[12:ptr], a.timestamps.b.stream[0:ptr-12])
		copy(buf[ptr:], compressed_data)
		a.b.stream = buf
	}

	writeCLMeta(a.num, uint32(ptr), a.errbound, a.ctype, a.b.stream)
	return nil
}

func (a *CLAppender) AppendHistogram(*HistogramAppender, int64, *histogram.Histogram, bool) (Chunk, bool, Appender, error) {
	panic("appended a histogram sample to a float chunk")
}

func (a *CLAppender) AppendFloatHistogram(*FloatHistogramAppender, int64, *histogram.FloatHistogram, bool) (Chunk, bool, Appender, error) {
	panic("appended a float histogram sample to a float chunk")
}

type CLIterator struct {
	tr                 bstreamReader
	QSimple8b_decoder  *Simple8bDecoder
	Huffman_decoder    *HuffmanDecoder
	Simplebits_decoder *SimplebitsDecoder
	BitPacking_decoder *BitPackingDecoder

	// When compressType is Machete, MOST or SZ3.
	decompressed_data []float64

	numTotal uint16
	numRead  uint16
	ctype    CompressType

	t         int64
	tDelta    int64
	val       float64
	quantizer int64
	errbound  float64

	err error
}

func (it *CLIterator) Reset(b []byte) bool {
	num, ptr, errbound, ctype := readCLMeta(b)

	if ctype == QSimple8b {
		it.QSimple8b_decoder = NewSimple8bDecoder(b[ptr:])
	} else if ctype == Huffman {
		it.Huffman_decoder = NewHuffmanDecoder(b, ptr)
	} else if ctype == Simplebits {
		it.Simplebits_decoder = NewSimplebitsDecoder(b[ptr:])
	} else if ctype == BitPacking {
		it.BitPacking_decoder = NewBitPackingDecoder(b[ptr:], int(num))
	} else {
		switch ctype {
		case SZ3:
			if num < uint16(SZ3_MinChunkSize) {
				it.decompressed_data = SZ_Decompress(1, b[ptr:], uint64(uint32(len(b))-ptr), 0, 0, 0, 0, uint64(SZ3_MinChunkSize))
			} else {
				it.decompressed_data = SZ_Decompress(1, b[ptr:], uint64(uint32(len(b))-ptr), 0, 0, 0, 0, uint64(num))
			}
		case MOST:
			it.decompressed_data = MOST_Decompress(b[ptr:], errbound)
		case Machete:
			it.decompressed_data = Machete_Decompress(b[ptr:], int64(uint32(len(b))-ptr), int64(num))
		default:
			return false
		}
	}

	it.tr = newBReader(b[12:ptr])
	it.numTotal = num
	it.errbound = errbound
	it.ctype = ctype

	it.numRead = 0
	it.val = 0
	it.quantizer = 0
	it.t = 0
	it.tDelta = 0
	it.err = nil

	return true
}

func (it *CLIterator) Next() ValueType {
	if it.err != nil || it.numRead == it.numTotal {
		return ValNone
	}
	err := it.nextTimestampDOD()
	if err != nil {
		it.err = err
		return ValNone
	}

	var fdelta uint64
	switch it.ctype {
	case QSimple8b:
		if !it.QSimple8b_decoder.Next() {
			return ValNone
		}
		fdelta = it.QSimple8b_decoder.Read()
	case Huffman:
		if !it.Huffman_decoder.Next() {
			return ValNone
		}
		fdelta = it.Huffman_decoder.Read()
	case Simplebits:
		if it.Simplebits_decoder.Next() != nil {
			return ValNone
		}
		fdelta = it.Simplebits_decoder.Read()
	case BitPacking:
		if it.BitPacking_decoder.Next() {
			return ValNone
		}
		fdelta = it.BitPacking_decoder.Read()
	default:
		it.val = it.decompressed_data[it.numRead]
	}

	if it.ctype == QSimple8b || it.ctype == Huffman || it.ctype == Simplebits || it.ctype == BitPacking || it.ctype == Auto {
		if fdelta%2 == 0 {
			it.val += it.errbound * float64(fdelta)
			it.quantizer += int64(fdelta) / 2
		} else {
			it.val -= it.errbound * float64(fdelta+1)
			it.quantizer -= int64(fdelta+1) / 2
		}
	}

	it.numRead += 1
	return ValFloat
}

func (it *CLIterator) Seek(t int64) ValueType {
	if it.err != nil {
		return ValNone
	}

	for t > it.t || it.numRead == 0 {
		if it.Next() == ValNone {
			return ValNone
		}
	}
	return ValFloat
}

func (it *CLIterator) At() (int64, float64) {
	return it.t, it.val
}

func (it *CLIterator) AtQuantizer() (int64, int64) {
	return it.t, it.quantizer
}

func (it *CLIterator) AtHistogram(*histogram.Histogram) (int64, *histogram.Histogram) {
	panic("cannot call xorIterator.AtHistogram")
}

func (it *CLIterator) AtFloatHistogram(*histogram.FloatHistogram) (int64, *histogram.FloatHistogram) {
	panic("cannot call xorIterator.AtFloatHistogram")
}

func (it *CLIterator) AtT() int64 {
	return it.t
}

func (it *CLIterator) Err() error {
	return it.err
}

func (it *CLIterator) ErrorBound() float64 {
	return it.errbound
}

func (it *CLIterator) CompressType() CompressType {
	return it.ctype
}

func (it *CLIterator) nextTimestampDOD() error {
	if it.numRead == 0 {
		t, err := binary.ReadVarint(&it.tr)
		if err != nil {
			it.err = err
			return err
		}
		it.t = t
	} else if it.numRead == 1 {
		t, err := binary.ReadVarint(&it.tr)
		if err != nil {
			it.err = err
			return err
		}
		it.tDelta = t
		it.t += it.tDelta
	} else {
		var d byte
		for i := 0; i < 4; i++ {
			d <<= 1
			bit, err := it.tr.readBitFast()
			if err != nil {
				bit, err = it.tr.readBit()
			}
			if err != nil {
				return err
			}
			if bit == zero {
				break
			}
			d |= 1
		}

		var sz uint8
		var dod int64
		switch d {
		case 0b0:
			// dod == 0
		case 0b10:
			sz = 14
		case 0b110:
			sz = 17
		case 0b1110:
			sz = 20
		case 0b1111:
			// Do not use fast because it's very unlikely it will succeed.
			bits, err := it.tr.readBits(64)
			if err != nil {
				return err
			}

			dod = int64(bits)
		}

		if sz != 0 {
			bits, err := it.tr.readBitsFast(sz)
			if err != nil {
				bits, err = it.tr.readBits(sz)
			}
			if err != nil {
				return err
			}

			// Account for negative numbers, which come back as high unsigned numbers.
			// See docs/bstream.md.
			if bits > (1 << (sz - 1)) {
				bits -= 1 << sz
			}
			dod = int64(bits)
		}

		it.tDelta += dod
		it.t += it.tDelta
	}
	return nil
}

func readCLMeta(b []byte) (uint16, uint32, float64, CompressType) {
	num := binary.BigEndian.Uint16(b)
	ptr := uint32(b[2])<<24 + uint32(b[3])<<16 + uint32(b[4])<<8 + uint32(b[5])
	errbound := uint32(b[6]&0x0f)<<24 + uint32(b[7])<<16 + uint32(b[8])<<8 + uint32(b[9])
	// CompressType indicates the type of Encoding method used.
	ctype := (b[6] & 0xf0) >> 4
	return num, ptr, float64(0.000001) * float64(errbound), CompressType(ctype)
}

func writeCLMeta(num uint16, ptr uint32, errbound float64, ctype CompressType, b []byte) {
	_ = b[9] // bounds check hint to compiler
	binary.BigEndian.PutUint16(b, num)
	for i := 0; i < 4; i += 1 {
		b[i+2] = byte(ptr >> (24 - 8*i))
	}
	for i := 0; i < 4; i += 1 {
		b[i+6] = byte(uint32(errbound*1000000) >> (24 - 8*i))
	}
	b[6] |= byte(ctype << 4)
}

type TimestampsDoD struct {
	b      *bstream
	t      int64
	tDelta int64
	num    int
}

func (td *TimestampsDoD) Append(t int64) {
	if td.num == 0 {
		buf := make([]byte, binary.MaxVarintLen64)
		for _, b := range buf[:binary.PutVarint(buf, t)] {
			td.b.writeByte(b)
		}
	} else if td.num == 1 {
		td.tDelta = t - td.t
		buf := make([]byte, binary.MaxVarintLen64)
		for _, b := range buf[:binary.PutVarint(buf, td.tDelta)] {
			td.b.writeByte(b)
		}
	} else {
		tDelta := t - td.t
		dod := tDelta - td.tDelta
		td.tDelta = tDelta
		switch {
		case dod == 0:
			td.b.writeBit(zero)
		case bitRange(dod, 14):
			td.b.writeBits(0b10, 2)
			td.b.writeBits(uint64(dod), 14)
		case bitRange(dod, 17):
			td.b.writeBits(0b110, 3)
			td.b.writeBits(uint64(dod), 17)
		case bitRange(dod, 20):
			td.b.writeBits(0b1110, 4)
			td.b.writeBits(uint64(dod), 20)
		default:
			td.b.writeBits(0b1111, 4)
			td.b.writeBits(uint64(dod), 64)
		}
	}
	td.t = t
	td.num += 1
}
