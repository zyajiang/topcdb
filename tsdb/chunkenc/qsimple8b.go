package chunkenc

import (
	"encoding/binary"

	"github.com/prometheus/prometheus/model/histogram"
)

type CompressType uint8

const (
	Simplebits CompressType = iota
	QSimple8b
	Huffman
	SZ3
	Machete
	MOST
	None
)

// Estimated compressed byte size of single float64 value for different compression methods.
const (
	Estimated_SZ3     float64 = 1
	Estimated_Machete float64 = 1
	Estimated_MOST    float64 = 1
)

var DefaultCtype CompressType = QSimple8b
var DefaultErrbound float64 = 0.01

var TEST_SZ3_MinChunkSize int = 1000

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
	num, _, _, _ := readCLMeta(c.b.stream)
	return int(num)
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
		fdeltas: NewSimple8bEncoder(),
		ctype:   DefaultCtype,

		v:        0,
		num:      0,
		errbound: DefaultErrbound,

		Huffman_buffer:    make([]int, 0),
		Simplebits_buffer: make([]uint64, 0),

		// TEST: only used when comparative testing
		TEST_buffer: make([]float64, 0),
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

func (c *CLChunk) ToHuffmanEncoding() error {
	num, ptr, errbound, CompressType := readCLMeta(c.b.stream)
	if CompressType == Huffman {
		return nil
	}

	iterator := c.Iterator(nil)
	var quantizer []int
	q0 := int64(0)
	for iterator.Next() == ValFloat {
		_, q := iterator.(*CLIterator).AtQuantizer()
		quantizer = append(quantizer, int(q-q0))
		q0 = q
	}
	b := HuffmanEncodeWithoutTimesMap(&quantizer, int(ptr))
	copy(b.stream, c.b.stream[:ptr])
	c.b = *b

	writeCLMeta(num, ptr, errbound, Huffman, c.b.stream)
	return nil
}

type CLAppender struct {
	b          *bstream
	timestamps *TimestampsDoD
	fdeltas    *Simple8bEncoder

	ctype CompressType

	v        int64
	num      uint32
	errbound float64

	Huffman_buffer    []int
	Simplebits_buffer []uint64

	// TEST: only used when comparative testing
	TEST_buffer []float64
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
	a.num += 1

	// TEST: only used when comparative testing
	if a.ctype != Huffman && a.ctype != QSimple8b && a.ctype != Simplebits {
		a.TEST_buffer = append(a.TEST_buffer, v)
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

	if a.ctype == Huffman {
		a.Huffman_buffer = append(a.Huffman_buffer, int(fdelta))
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
		a.fdeltas.Write(uint64(fdelta))
	} else if a.ctype == Simplebits {
		a.Simplebits_buffer = append(a.Simplebits_buffer, uint64(fdelta))
	}
}

// AppendQuantizer appends the quantized integer value directly, which happens when compaction.
func (a *CLAppender) AppendQuantizer(t int64, v int64) {
	// Step1: Quantize the float value.
	// Step2: Predict the current value from the previous value.
	// Calculate the delta between the quantized value and the previous one.
	fdelta := v - a.v
	a.v = v

	// Zigzag encode the delta.
	if fdelta >= 0 {
		fdelta <<= 1
	} else {
		fdelta = -2*fdelta - 1
	}

	// Step3: Bit-packing encode the delta.
	a.fdeltas.Write(uint64(fdelta))

	// Write the timestamp.
	a.timestamps.Append(t)

	a.num += 1
}

func (a *CLAppender) NumSamples() int {
	return int(a.num)
}

func (a *CLAppender) TimestampSize() int {
	return len(a.timestamps.b.stream)
}

func (a *CLAppender) EstimatedSize() int {
	switch a.ctype {
	case SZ3:
		return len(a.timestamps.b.stream) + int(float64(len(a.TEST_buffer))*Estimated_SZ3)
	case Machete:
		return len(a.timestamps.b.stream) + int(float64(len(a.TEST_buffer))*Estimated_Machete)
	case MOST:
		return len(a.timestamps.b.stream) + int(float64(len(a.TEST_buffer))*Estimated_MOST)
	default:
		return len(a.timestamps.b.stream) + a.fdeltas.EstimatedSize()
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

	if a.ctype == Huffman {
		// when CompressType is Huffman
		b := HuffmanEncodeWithoutTimesMap(&a.Huffman_buffer, int(ptr))
		copy(b.stream[12:ptr], a.timestamps.b.stream[0:ptr-12])
		a.b.stream = b.stream
		a.b.count = b.count
		writeCLMeta(a.num, uint32(ptr), a.errbound, a.ctype, a.b.stream)
		return nil
	}

	if a.ctype == QSimple8b {
		compressed_data, err = a.fdeltas.Bytes()
		if err != nil {
			return err
		}
	} else if a.ctype == Simplebits {
		bitCounts, maxBits := BitStatistics(a.Simplebits_buffer)
		compressed_data = PackingAll(a.Simplebits_buffer, BitSelectors(bitCounts, Simplebits_MaxSegmentsNum, maxBits)).bytes()
	} else {
		// TEST: when comparative testing
		var outSize uint64
		switch a.ctype {
		case SZ3:
			if a.num < uint32(TEST_SZ3_MinChunkSize) {
				compressed_data = SZ_Compress(1, a.TEST_buffer, &outSize, 0, a.errbound, 0, 0, 0, 0, 0, 0, uint64(TEST_SZ3_MinChunkSize))
			} else {
				compressed_data = SZ_Compress(1, a.TEST_buffer, &outSize, 0, a.errbound, 0, 0, 0, 0, 0, 0, uint64(a.num))
			}
		case MOST:
			compressed_data = MOST_Compress(a.TEST_buffer, a.errbound, 5)
		case Machete:
			compressed_data = Machete_Compress(a.TEST_buffer, int64(len(a.TEST_buffer)), a.errbound)
		}
	}

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
	tr      bstreamReader
	fdeltas *Simple8bDecoder
	entropy *HuffmanDecoder

	// TEST: when comparative testing
	decompressed_data []float64

	numTotal uint32
	numRead  uint32
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
		it.fdeltas = NewSimple8bDecoder(b[ptr:])
	} else if ctype == Huffman {
		it.entropy = NewHuffmanDecoder(b, ptr)
	} else {
		switch ctype {
		case SZ3:
			if num < uint32(TEST_SZ3_MinChunkSize) {
				it.decompressed_data = SZ_Decompress(1, b[ptr:], uint64(uint32(len(b))-ptr), 0, 0, 0, 0, uint64(TEST_SZ3_MinChunkSize))
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

	switch it.ctype {
	case QSimple8b:
		if !it.fdeltas.Next() {
			return ValNone
		}
		fdelta := it.fdeltas.Read()
		if fdelta%2 == 0 {
			it.val += it.errbound * float64(fdelta)
			it.quantizer += int64(fdelta) / 2
		} else {
			it.val -= it.errbound * float64(fdelta+1)
			it.quantizer -= int64(fdelta+1) / 2
		}
	case Huffman:
		if !it.entropy.Next() {
			return ValNone
		}
		it.quantizer = it.entropy.Read()
		it.val = 2 * it.errbound * float64(it.quantizer)
	default:
		it.val = it.decompressed_data[it.numRead]
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

func readCLMeta(b []byte) (uint32, uint32, float64, CompressType) {
	num := uint32(b[0])<<24 + uint32(b[1])<<16 + uint32(b[2])<<8 + uint32(b[3])
	ptr := uint32(b[4])<<24 + uint32(b[5])<<16 + uint32(b[6])<<8 + uint32(b[7])
	errbound := uint32(b[8]&0x0f)<<24 + uint32(b[9])<<16 + uint32(b[10])<<8 + uint32(b[11])
	// CompressType indicates the type of Encoding method used.
	ctype := (b[8] & 0xf0) >> 4
	return num, ptr, float64(0.00001) * float64(errbound), CompressType(ctype)
}

func writeCLMeta(num uint32, ptr uint32, errbound float64, ctype CompressType, b []byte) {
	_ = b[11]
	for i := 0; i < 4; i += 1 {
		b[i] = byte(num >> (24 - 8*i))
	}
	for i := 0; i < 4; i += 1 {
		b[i+4] = byte(ptr >> (24 - 8*i))
	}
	for i := 0; i < 4; i += 1 {
		b[i+8] = byte(uint32(errbound*100000) >> (24 - 8*i))
	}
	b[8] |= byte(ctype << 4)
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
