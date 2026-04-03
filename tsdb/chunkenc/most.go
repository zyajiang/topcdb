package chunkenc

import (
	"encoding/binary"
	"math"
)

type Segment struct {
	slope uint16
	value uint16

	start_id int
	end_id   int
}

type BaseValues struct {
	b      *bstream
	num    uint32
	tDelta int64
	t      int64

	baser   *bstreamReader
	numRead uint32
}

func (a *BaseValues) append(val1 uint16, val2 uint16, t int64) {
	if a.num == 0 {
		buf := make([]byte, binary.MaxVarintLen64)
		for _, b := range buf[:binary.PutVarint(buf, t)] {
			a.b.writeByte(b)
		}
	} else if a.num == 1 {
		a.tDelta = t - a.t
		buf := make([]byte, binary.MaxVarintLen64)
		for _, b := range buf[:binary.PutVarint(buf, a.tDelta)] {
			a.b.writeByte(b)
		}
	} else {
		dod := (t - a.t) - int64(a.tDelta)
		a.tDelta = t - a.t
		switch {
		case dod == 0:
			a.b.writeBit(zero)
		case bitRange(dod, 14):
			a.b.writeBits(0b10, 2)
			a.b.writeBits(uint64(dod), 14)
		case bitRange(dod, 17):
			a.b.writeBits(0b110, 3)
			a.b.writeBits(uint64(dod), 17)
		case bitRange(dod, 20):
			a.b.writeBits(0b1110, 4)
			a.b.writeBits(uint64(dod), 20)
		default:
			a.b.writeBits(0b1111, 4)
			a.b.writeBits(uint64(dod), 64)
		}
	}
	a.t = t
	a.num += 1

	a.b.writeBits(uint64(val1), 16)
	a.b.writeBits(uint64(val2), 16)
}

func (a *BaseValues) next() (float64, float64, int64, error) {
	if a.numRead == 0 {
		t, err := binary.ReadVarint(a.baser)
		if err != nil {
			return 0, 0, 0, err
		}
		a.t = t
	} else if a.numRead == 1 {
		t, err := binary.ReadVarint(a.baser)
		if err != nil {
			return 0, 0, 0, err
		}
		a.tDelta = t
		a.t += int64(a.tDelta)
	} else {
		var d byte
		for i := 0; i < 4; i++ {
			d <<= 1
			bit, err := a.baser.readBitFast()
			if err != nil {
				bit, err = a.baser.readBit()
			}
			if err != nil {
				return 0, 0, 0, err
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
			bits, err := a.baser.readBits(64)
			if err != nil {
				return 0, 0, 0, err
			}

			dod = int64(bits)
		}

		if sz != 0 {
			bits, err := a.baser.readBitsFast(sz)
			if err != nil {
				bits, err = a.baser.readBits(sz)
			}
			if err != nil {
				return 0, 0, 0, err
			}

			// Account for negative numbers, which come back as high unsigned numbers.
			// See docs/bstream.md.
			if bits > (1 << (sz - 1)) {
				bits -= 1 << sz
			}
			dod = int64(bits)
		}

		a.tDelta += dod
		a.t += a.tDelta
	}

	a.numRead += 1
	slope, err := a.baser.readBits(16)
	if err != nil {
		return 0, 0, 0, err
	}
	value, err := a.baser.readBits(16)
	if err != nil {
		return 0, 0, 0, err
	}
	slope_f64 := float64(math.Float32frombits(uint32(slope) << 16))
	value_f64 := float64(math.Float32frombits(uint32(value) << 16))
	return slope_f64, value_f64, a.t, nil
}

func MOST_Compress(input []float64, errbound float64, MIN_SEGMENT_LEN int) []byte {
	var vdelta uint32
	var vdeltas []uint64
	var segments []Segment

	basestream := BaseValues{
		b:   &bstream{make([]byte, 0), 0},
		num: 0,
	}

	segments = segmentationCSC(input, 16*errbound, MIN_SEGMENT_LEN)
	segItr, tvItr := 0, 0
	for segItr < len(segments) {
		seg_slope := float64(math.Float32frombits(uint32(segments[segItr].slope) << 16))
		seg_value := float64(math.Float32frombits(uint32(segments[segItr].value) << 16))
		// write segment information
		// | end time | slope | start value |
		basestream.append(segments[segItr].slope, segments[segItr].value, int64(segments[segItr].end_id))

		for tvItr <= segments[segItr].end_id {
			vpredict := seg_slope*float64(tvItr-segments[segItr].start_id) + seg_value
			signed_vdelta := int32(math.Floor((input[tvItr]-vpredict)/(2*errbound) + 0.5))
			if signed_vdelta >= 0 {
				vdelta = uint32(signed_vdelta * 2)
			} else {
				vdelta = uint32(-signed_vdelta*2 - 1)
			}
			vdeltas = append(vdeltas, uint64(vdelta))

			tvItr += 1
		}
		segItr += 1
	}

	basestream.b.count = 0
	vdeltabyte, err := EncodeAll(vdeltas)
	if err != nil {
		return nil
	}
	for _, b := range vdeltabyte[:] {
		basestream.b.writeByte(b)
	}
	basestream.b.count = 0
	basestream.b.writeBits(uint64(len(vdeltabyte)), 32)
	basestream.b.writeBits(uint64(len(segments)), 32)
	return basestream.b.bytes()
}

func MOST_Decompress(input []byte, errbound float64) []float64 {
	var (
		output     []float64
		segmentLen uint32
		vdeltaLen  uint32
		decoder    *Simple8bDecoder
		br         bstreamReader
	)

	segmentLen = binary.BigEndian.Uint32(input[len(input)-4:])
	vdeltaLen = binary.BigEndian.Uint32(input[len(input)-8 : len(input)-4])
	decoder = NewSimple8bDecoder(input[len(input)-8-int(vdeltaLen) : len(input)-8])
	br = newBReader(input[:len(input)-8-int(vdeltaLen)])

	baser := BaseValues{
		numRead: 0,
		t:       0,
		tDelta:  0,
		baser:   &br,
	}

	startId, tvItr := 0, 0
	for segItr := 0; segItr < int(segmentLen); segItr++ {
		slope, value, endId, err := baser.next()
		if err != nil {
			return nil
		}

		for ; tvItr <= int(endId); tvItr++ {
			if !decoder.Next() {
				return nil
			}
			vdelta := decoder.Read()
			vpredict := slope*float64(tvItr-startId) + value
			var signed_vdelta float64
			if vdelta%2 == 0 {
				signed_vdelta = float64(vdelta)
			} else {
				signed_vdelta = -(float64(vdelta) + 1)
			}
			output = append(output, signed_vdelta*errbound+vpredict)
		}
		startId = int(endId) + 1
	}
	return output
}

func segmentationCSC(input []float64, segbound float64, MIN_SEGMENT_LEN int) []Segment {
	var segments []Segment
	var start, next, spliter int
	start, next, spliter = 0, 1, -1
	start_value_u16 := uint16(math.Float32bits(float32(input[start])) >> 16)
	start_value := float64(math.Float32frombits(uint32(start_value_u16) << 16))
	kmin, kmax := -math.MaxFloat64, math.MaxFloat64

	for next < len(input) {
		// Segment input tvpairs and store key information
		kadd_next := (input[next] + segbound - start_value) / (float64(next - start))
		ksub_next := (input[next] - segbound - start_value) / (float64(next - start))
		kmax_next := math.Max(kadd_next, ksub_next)
		kmin_next := math.Min(kadd_next, ksub_next)

		if kmin_next <= kmax && kmax_next >= kmin {
			if spliter >= 0 {
				spliter = -1
			}
			kmin = math.Max(kmin, kmin_next)
			kmax = math.Min(kmax, kmax_next)
			next += 1
		} else if spliter < 0 {
			spliter = next
			next += 1
		} else {
			if spliter-start >= MIN_SEGMENT_LEN {
				if (len(segments) == 0 && start > 0) || (len(segments) > 0 && segments[len(segments)-1].end_id < start-1) {
					new_start_id, slope_temp := 0, float64(0)
					if len(segments) > 0 {
						new_start_id = segments[len(segments)-1].end_id + 1
					}
					new_end_id := start - 1
					if new_end_id != new_start_id {
						slope_temp = (input[new_end_id] - input[new_start_id]) / (float64(new_end_id - new_start_id))
					}
					segments = append(segments, Segment{
						slope: uint16(math.Float32bits(float32(slope_temp)) >> 16),
						value: uint16(math.Float32bits(float32(input[new_start_id])) >> 16),

						start_id: new_start_id,
						end_id:   new_end_id,
					})
				}
				segments = append(segments, Segment{
					slope: slopeFromRangek(kmax, kmin),
					value: start_value_u16,

					start_id: start,
					end_id:   spliter,
				})
				start = spliter + 1
			} else {
				start += 1
			}

			next = start + 1
			spliter = -1
			start_value_u16 = uint16(math.Float32bits(float32(input[start])) >> 16)
			start_value = float64(math.Float32frombits(uint32(start_value_u16) << 16))
			kmin, kmax = -math.MaxFloat64, math.MaxFloat64
		}
	}
	if len(segments) == 0 || segments[len(segments)-1].end_id < len(input)-1 {
		new_start_id, new_end_id, slope_temp := 0, len(input)-1, float64(0)
		if len(segments) > 0 {
			new_start_id = segments[len(segments)-1].end_id + 1
		}
		if new_end_id != new_start_id {
			slope_temp = (input[new_end_id] - input[new_start_id]) / (float64(new_end_id - new_start_id))
		}
		segments = append(segments, Segment{
			slope: uint16(math.Float32bits(float32(slope_temp)) >> 16),
			value: uint16(math.Float32bits(float32(input[new_start_id])) >> 16),

			start_id: new_start_id,
			end_id:   new_end_id,
		})
	}

	return segments
}

func slopeFromRangek(maxk float64, mink float64) uint16 {
	var uintf uint16

	uintf = uint16(math.Float32bits(float32((maxk+mink)/2)) >> 16)
	return uintf
}
