// Copyright 2017 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/bits"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prometheus/common/promslog"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/promqltest"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
)

func BenchmarkAlgorithmCompression(b *testing.B) {
	dl := tsdb.NewDatasetLoader()

	// ErrorBound is absolute error bound for compression algorithms.
	ErrorBound := 1e-4
	// The Whole dataset will be divided into chunks of ChunkSize for compression.
	ChunkSize := 1000
	Datasets := map[string]string{
		// "pamapv2": "/home/jzj/Datasets/PAMAP2_Dataset",
		"pamapv2": "/home/jzj/Datasets/PAMAP2_Dataset_mini",
		// "uci_gas":           "/home/jzj/Datasets/UCI_GAS",
		// "ucr":               "/home/jzj/Datasets/UCRAchive_2018",
		// "ett": "/home/jzj/Datasets/ETT",
		// "household_voltage": "/home/jzj/Datasets/Household_Voltage",
		// "wisdm":       "/home/jzj/Datasets/WISDM_Dataset/raw",
		// "geolife":     "/home/jzj/Datasets/Geolife_Trajectories_1_3/Data/",
		// "electricity": "/home/jzj/Datasets/Electricity",
	}
	// LosslessErrbound := map[string]float64{
	// 	"pamapv2":           1e-9,
	// 	"uci_gas":           1e-2,
	// 	"ucr":               1e-11,
	// 	"ett":               1e-18,
	// 	"household_voltage": 1e-3,
	// 	"wisdm":             1e-6,
	// 	"geolife":           1e-10,
	// 	"electricity":       1e-6,
	// }

	for name, path := range Datasets {
		fmt.Printf("Dataset: %s\n", name)
		// ErrorBound = LosslessErrbound[name]

		if err := dl.ReadDataset(name, path); err != nil {
			return
		}

		var uncompressed_total_bytes int
		var timestamp_total_bytes int
		var sz_total_bytes, most_total_bytes, machete_total_bytes, gorilla_total_bytes int
		var simple8b_total_bytes, simplebits_total_bytes, bitpacking_total_bytes, varint_total_bytes, pfor_total_bytes, huffman_total_bytes, auto_total_bytes int

		for _, vals := range dl.Samples {
			currChunkSize := min(ChunkSize, len(vals))

			for batch := 0; batch*currChunkSize < len(vals); batch += 1 {
				uncompressed_data := make([]float64, 0, currChunkSize)

				// Gorilla
				gorilla_chk := chunkenc.NewXORChunk()
				gorilla_app, _ := gorilla_chk.Appender()

				// Simplebits
				simplebits_chk := chunkenc.NewCLChunk()
				simplebits_app, _ := simplebits_chk.Appender()
				simplebits_app.(*chunkenc.CLAppender).SetCompressType(chunkenc.Simplebits)
				simplebits_app.(*chunkenc.CLAppender).SetErrorBound(ErrorBound)

				// Auto
				auto_chk := chunkenc.NewCLChunk()
				auto_app, _ := auto_chk.Appender()
				auto_app.(*chunkenc.CLAppender).SetCompressType(chunkenc.Auto)
				auto_app.(*chunkenc.CLAppender).SetErrorBound(ErrorBound)

				// Huffman
				huffman_chk := chunkenc.NewCLChunk()
				huffman_app, _ := huffman_chk.Appender()
				huffman_app.(*chunkenc.CLAppender).SetCompressType(chunkenc.Huffman)
				huffman_app.(*chunkenc.CLAppender).SetErrorBound(ErrorBound)

				// BitPacking
				bitpacking_chk := chunkenc.NewCLChunk()
				bitpacking_app, _ := bitpacking_chk.Appender()
				bitpacking_app.(*chunkenc.CLAppender).SetCompressType(chunkenc.BitPacking)
				bitpacking_app.(*chunkenc.CLAppender).SetErrorBound(ErrorBound)

				// Varint
				varint_chk := chunkenc.NewCLChunk()
				varint_app, _ := varint_chk.Appender()
				varint_app.(*chunkenc.CLAppender).SetCompressType(chunkenc.Varint)
				varint_app.(*chunkenc.CLAppender).SetErrorBound(ErrorBound)

				// PFor
				pfor_chk := chunkenc.NewCLChunk()
				pfor_app, _ := pfor_chk.Appender()
				pfor_app.(*chunkenc.CLAppender).SetCompressType(chunkenc.PFor)
				pfor_app.(*chunkenc.CLAppender).SetErrorBound(ErrorBound)

				// Simple8b
				simple8b_chk := chunkenc.NewCLChunk()
				simple8b_app, _ := simple8b_chk.Appender()
				simple8b_app.(*chunkenc.CLAppender).SetCompressType(chunkenc.QSimple8b)
				simple8b_app.(*chunkenc.CLAppender).SetErrorBound(ErrorBound)

				for i := batch * currChunkSize; i < (batch+1)*currChunkSize && i < len(vals); i += 1 {
					uncompressed_data = append(uncompressed_data, vals[i].V)
					gorilla_app.Append(int64(i), vals[i].V)
					simple8b_app.Append(int64(i), vals[i].V)
					simplebits_app.Append(int64(i), vals[i].V)
					bitpacking_app.Append(int64(i), vals[i].V)
					varint_app.Append(int64(i), vals[i].V)
					pfor_app.Append(int64(i), vals[i].V)
					huffman_app.Append(int64(i), vals[i].V)
					auto_app.Append(int64(i), vals[i].V)
				}

				simple8b_app.(*chunkenc.CLAppender).Compact()
				simplebits_app.(*chunkenc.CLAppender).Compact()
				huffman_app.(*chunkenc.CLAppender).Compact()
				bitpacking_app.(*chunkenc.CLAppender).Compact()
				varint_app.(*chunkenc.CLAppender).Compact()
				pfor_app.(*chunkenc.CLAppender).Compact()
				auto_app.(*chunkenc.CLAppender).Compact()

				// SZ, MOST, Machete
				// var outSize uint64
				// comp_sz := chunkenc.SZ_Compress(1, uncompressed_data, &outSize, 0, ErrorBound, 0, 0, 0, 0, 0, 0, uint64(ChunkSize))
				// comp_most := chunkenc.MOST_Compress(uncompressed_data, ErrorBound, 5)
				// comp_machete := chunkenc.Machete_Compress(uncompressed_data, int64(len(uncompressed_data)), ErrorBound)

				// verify decompressed data correctness
				// dec_sz := chunkenc.SZ_Decompress(1, comp_sz, uint64(len(comp_sz)), 0, 0, 0, 0, uint64(ChunkSize))
				// dec_most := chunkenc.MOST_Decompress(comp_most, ErrorBound)
				// dec_machete := chunkenc.Machete_Decompress(comp_machete, int64(len(comp_machete)), int64(ChunkSize))

				// for i := 0; i < len(uncompressed_data); i += 1 {
				// if math.Abs(dec_sz[i]-uncompressed_data[i]) > (1+1e-6)*ErrorBound {
				// 	fmt.Printf("SZ Decompression Error > (%d,%f)(%f)\n", i, dec_sz[i], uncompressed_data[i])
				// }
				// if math.Abs(dec_most[i]-uncompressed_data[i]) > (1+1e-6)*ErrorBound {
				// 	fmt.Printf("MOST Decompression Error > (%d,%f)(%f)\n", i, dec_most[i], uncompressed_data[i])
				// }
				// if math.Abs(dec_machete[i]-uncompressed_data[i]) > (1+1e-6)*ErrorBound {
				// fmt.Printf("Machete Decompression Error > (%d,%f)(%f)\n", i, dec_machete[i], uncompressed_data[i])
				// }
				// }

				// Timestamp bytes are the same for all algorithms
				timestamp_total_bytes = simple8b_app.(*chunkenc.CLAppender).TimestampSize()

				// Total bytes for each algorithm
				gorilla_total_bytes += len(gorilla_chk.Bytes()) - timestamp_total_bytes
				simple8b_total_bytes += simple8b_app.(*chunkenc.CLAppender).FloatSize()
				simplebits_total_bytes += simplebits_app.(*chunkenc.CLAppender).FloatSize()
				auto_total_bytes += auto_app.(*chunkenc.CLAppender).FloatSize()
				// sz_total_bytes += len(comp_sz)
				// most_total_bytes += len(comp_most)
				// machete_total_bytes += len(comp_machete)
				bitpacking_total_bytes += bitpacking_app.(*chunkenc.CLAppender).FloatSize()
				varint_total_bytes += varint_app.(*chunkenc.CLAppender).FloatSize()
				pfor_total_bytes += pfor_app.(*chunkenc.CLAppender).FloatSize()
				huffman_total_bytes += huffman_app.(*chunkenc.CLAppender).FloatSize()
				uncompressed_total_bytes += currChunkSize * 8
			}
		}

		fmt.Printf(" > uncompressed_total_bytes: %d\n", uncompressed_total_bytes)
		fmt.Printf(" > sz_total_bytes: %d, sz compression ratio: %f\n", sz_total_bytes, float64(uncompressed_total_bytes)/float64(sz_total_bytes))
		fmt.Printf(" > most_total_bytes: %d, most compression ratio: %f\n", most_total_bytes, float64(uncompressed_total_bytes)/float64(most_total_bytes))
		fmt.Printf(" > machete_total_bytes: %d, machete compression ratio: %f\n", machete_total_bytes, float64(uncompressed_total_bytes)/float64(machete_total_bytes))
		fmt.Printf(" > gorilla_total_bytes: %d, gorilla compression ratio: %f\n", gorilla_total_bytes, float64(uncompressed_total_bytes)/float64(gorilla_total_bytes))
		fmt.Printf(" > simple8b_total_bytes: %d, simple8b compression ratio: %f\n", simple8b_total_bytes, float64(uncompressed_total_bytes)/float64(simple8b_total_bytes))
		fmt.Printf(" > simplebits_total_bytes: %d, simplebits compression ratio: %f\n", simplebits_total_bytes, float64(uncompressed_total_bytes)/float64(simplebits_total_bytes))
		fmt.Printf(" > bitpacking_total_bytes: %d, bitpacking compression ratio: %f\n", bitpacking_total_bytes, float64(uncompressed_total_bytes)/float64(bitpacking_total_bytes))
		fmt.Printf(" > varint_total_bytes: %d, varint compression ratio: %f\n", varint_total_bytes, float64(uncompressed_total_bytes)/float64(varint_total_bytes))
		fmt.Printf(" > pfor_total_bytes: %d, pfor compression ratio: %f\n\n\n", pfor_total_bytes, float64(uncompressed_total_bytes)/float64(pfor_total_bytes))
		fmt.Printf(" > huffman_total_bytes: %d, huffman compression ratio: %f\n\n\n", huffman_total_bytes, float64(uncompressed_total_bytes)/float64(huffman_total_bytes))
		fmt.Printf(" > auto_total_bytes: %d, auto compression ratio: %f\n\n\n", auto_total_bytes, float64(uncompressed_total_bytes)/float64(auto_total_bytes))
	}
}

func BenchmarkIntegerCompression(b *testing.B) {
	dl := tsdb.NewDatasetLoader()

	// ErrorBound is absolute error bound for compression algorithms.
	ErrorBounds := []float64{1e-2, 1e-3, 1e-4, 1e-5, 1e-6}
	for _, ErrorBound := range ErrorBounds {
		fmt.Printf("ErrorBound: %f\n", ErrorBound)
		// The Whole dataset will be divided into chunks of ChunkSize for compression.
		ChunkSize := 1000
		Datasets := map[string]string{
			"pamapv2": "/home/jzj/Datasets/PAMAP2_Dataset",
			// "pamapv2":           "/home/jzj/Datasets/PAMAP2_Dataset_mini",
			"ett":   "/home/jzj/Datasets/ETT",
			"wisdm": "/home/jzj/Datasets/WISDM_Dataset/raw",
			// "geolife":     "/home/jzj/Datasets/Geolife_Trajectories_1_3/Data/",
			"electricity": "/home/jzj/Datasets/Electricity",
		}

		bitWidth := func(v uint64) int {
			if v == 0 {
				return 1
			}
			return bits.Len64(v)
		}

		for name, path := range Datasets {
			fmt.Printf("Dataset: %s\n", name)
			if err := dl.ReadDataset(name, path); err != nil {
				return
			}

			var uncompressed_total_bytes int
			var simple8b_total_bytes, simplebits_total_bytes, bitpacking_total_bytes, varint_total_bytes, pfor_total_bytes, gorilla_total_bytes, huffman_total_bytes, optimal_total_bytes int
			var simple8b_total_time, simplebits_total_time, bitpacking_total_time, varint_total_time, pfor_total_time, gorilla_total_time, huffman_total_time time.Duration
			// var simple8b_meta_total_bytes, simplebits_meta_total_bytes, bitpacking_meta_total_bytes, varint_meta_total_bytes, pfor_meta_total_bytes int

			for _, vals := range dl.Samples {
				data_delta := make([]int64, 0, len(vals))
				data_zigzag := make([]uint64, 0, len(vals))
				curr_val := int64(0)
				for i := 0; i < len(vals); i += 1 {
					var f2i int64
					if vals[i].V >= 0 {
						f2i = int64(vals[i].V/(2*ErrorBound) + 0.5)
					} else {
						f2i = int64(vals[i].V/(2*ErrorBound) - 0.5)
					}
					fdelta := f2i - curr_val
					curr_val = f2i

					data_delta = append(data_delta, fdelta)

					if fdelta >= 0 {
						fdelta <<= 1
					} else {
						fdelta = -2*fdelta - 1
					}

					data_zigzag = append(data_zigzag, uint64(fdelta))
					optimal_total_bytes += bitWidth(uint64(fdelta))
				}

				currChunkSize := min(ChunkSize, len(vals))

				// Simplebits
				// simplebits_chks := make([][]byte, 0, (len(vals)+currChunkSize-1)/currChunkSize)
				start := time.Now()
				for batch := 0; batch*currChunkSize < len(vals); batch += 1 {
					begin, end := batch*currChunkSize, min((batch+1)*currChunkSize, len(vals))
					bitCounters, maxBits := chunkenc.BitStatistics(data_zigzag[begin:end])
					bitSelectors, _ := chunkenc.BitSelectors(bitCounters, chunkenc.Simplebits_MaxSegmentsNum, maxBits, end-begin)
					simplebits_chk := chunkenc.PackingAll(data_zigzag[begin:end], bitSelectors)
					simplebits_total_bytes += simplebits_chk.Len()
					// simplebits_meta_total_bytes += simplebits_meta_bytes

					// simplebits_chks = append(simplebits_chks, simplebits_chk.Bytes())
				}
				simplebits_total_time += time.Since(start)

				// start := time.Now()
				// for _, chk := range simplebits_chks {
				// 	br := chunkenc.NewBReader(chk)
				// 	chunkenc.UnPackingAll(&br, len(chk))
				// }
				// simplebits_total_time += time.Since(start)

				// Huffman
				// huffman_chks := make([]*chunkenc.CLChunk, 0, (len(vals)+currChunkSize-1)/currChunkSize)
				// start = time.Now()
				// for batch := 0; batch*currChunkSize < len(vals); batch += 1 {
				// 	huffman_chk := chunkenc.NewCLChunk()
				// 	huffman_app, _ := huffman_chk.Appender()
				// 	huffman_app.(*chunkenc.CLAppender).SetErrorBound(ErrorBound)
				// 	huffman_app.(*chunkenc.CLAppender).SetCompressType(chunkenc.Huffman)
				// 	begin, end := batch*currChunkSize, min((batch+1)*currChunkSize, len(vals))
				// 	for j := begin; j < end; j += 1 {
				// 		huffman_app.Append(int64(j), vals[j].v)
				// 	}
				// 	huffman_app.(*chunkenc.CLAppender).Compact()
				// 	huffman_total_bytes += huffman_app.(*chunkenc.CLAppender).FloatSize()

				// 	// huffman_chks = append(huffman_chks, huffman_chk)
				// }
				// huffman_total_time += time.Since(start)

				// start = time.Now()
				// for _, chk := range huffman_chks {
				// 	it := chk.Iterator(nil)
				// 	for it.Next() != chunkenc.ValNone {
				// 		it.At()
				// 	}
				// }
				// huffman_total_time += time.Since(start)

				// Gorilla
				// gorilla_chks := make([]*chunkenc.XORChunk, 0, (len(vals)+currChunkSize-1)/currChunkSize)
				// start = time.Now()
				// for batch := 0; batch*currChunkSize < len(vals); batch += 1 {
				// 	gorilla_chk := chunkenc.NewXORChunk()
				// 	gorilla_app, _ := gorilla_chk.Appender()
				// 	begin, end := batch*currChunkSize, min((batch+1)*currChunkSize, len(vals))
				// 	for j := begin; j < end; j += 1 {
				// 		gorilla_app.Append(int64(j), vals[j].v)
				// 	}
				// 	gorilla_total_bytes += len(gorilla_chk.Bytes()) - (end-begin)/8 - 8

				// 	// gorilla_chks = append(gorilla_chks, gorilla_chk)
				// }
				// gorilla_total_time += time.Since(start)

				// start = time.Now()
				// for _, chk := range gorilla_chks {
				// 	it := chk.Iterator(nil)
				// 	for it.Next() != chunkenc.ValNone {
				// 		it.At()
				// 	}
				// }
				// gorilla_total_time += time.Since(start)

				// BitPacking
				// bitpacking_chks := make([][]byte, 0, (len(vals)+currChunkSize-1)/currChunkSize)
				// start = time.Now()
				// for batch := 0; batch*currChunkSize < len(vals); batch += 1 {
				// 	begin, end := batch*currChunkSize, min((batch+1)*currChunkSize, len(vals))
				// 	bitpacking_chk := chunkenc.BitPackingAll(data_zigzag[begin:end])
				// 	bitpacking_total_bytes += bitpacking_chk.Len()
				// 	// bitpacking_meta_total_bytes += bitpacking_meta_bytes

				// 	// bitpacking_chks = append(bitpacking_chks, bitpacking_chk.Bytes())
				// }
				// bitpacking_total_time += time.Since(start)

				// start = time.Now()
				// for _, chk := range bitpacking_chks {
				// 	br := chunkenc.NewBReader(chk)
				// 	chunkenc.UnBitPackingAll(&br, len(chk))
				// }
				// bitpacking_total_time += time.Since(start)

				// Varint
				// varint_chks := make([][]byte, 0, (len(vals)+currChunkSize-1)/currChunkSize)
				// start = time.Now()
				// for batch := 0; batch*currChunkSize < len(vals); batch += 1 {
				// 	begin, end := batch*currChunkSize, min((batch+1)*currChunkSize, len(vals))
				// 	varint_chk := chunkenc.VarintPackingAll(data_zigzag[begin:end])
				// 	varint_total_bytes += varint_chk.Len()
				// 	// varint_meta_total_bytes += varint_meta_bytes

				// 	// varint_chks = append(varint_chks, varint_chk.Bytes())
				// }
				// varint_total_time += time.Since(start)

				// start = time.Now()
				// for _, chk := range varint_chks {
				// 	br := chunkenc.NewBReader(chk)
				// 	chunkenc.UnVarintPackingAll(&br, len(chk))
				// }
				// varint_total_time += time.Since(start)

				// PFor
				// pfor_chks := make([][]byte, 0, (len(vals)+currChunkSize-1)/currChunkSize)
				// start = time.Now()
				// for batch := 0; batch*currChunkSize < len(vals); batch += 1 {
				// 	begin, end := batch*currChunkSize, min((batch+1)*currChunkSize, len(vals))
				// 	pfor_chk := chunkenc.PForPackingAll(data_delta[begin:end])
				// 	pfor_total_bytes += pfor_chk.Len()
				// 	// pfor_meta_total_bytes += pfor_meta_bytes

				// 	// pfor_chks = append(pfor_chks, pfor_chk.Bytes())
				// }
				// pfor_total_time += time.Since(start)

				// start = time.Now()
				// for _, chk := range pfor_chks {
				// 	br := chunkenc.NewBReader(chk)
				// 	chunkenc.UnPForPackingAll(&br, len(chk))
				// }
				// pfor_total_time += time.Since(start)

				// Simple8b
				// simple8b_chks := make([][]uint64, 0, (len(vals)+currChunkSize-1)/currChunkSize)
				start = time.Now()
				for batch := 0; batch*currChunkSize < len(vals); batch += 1 {
					begin, end := batch*currChunkSize, min((batch+1)*currChunkSize, len(vals))
					simple8b_chk, _ := chunkenc.EncodeAll(data_zigzag[begin:end])
					simple8b_total_bytes += len(simple8b_chk)
					// simple8b_meta_total_bytes += simple8b_meta_bytes

					// simple8b_chks = append(simple8b_chks, simple8b_chk)
				}
				simple8b_total_time += time.Since(start)

				// start = time.Now()
				// for _, chk := range simple8b_chks {
				// 	uncompressed_chk := make([]uint64, currChunkSize)
				// 	chunkenc.DecodeAll(uncompressed_chk, chk)
				// }
				// simple8b_total_time += time.Since(start)

				uncompressed_total_bytes += len(vals) * 8
			}

			fmt.Printf(" > uncompressed_total_bytes: %d, optimal compression ratio: %f\n",
				uncompressed_total_bytes,
				float64(uncompressed_total_bytes*8)/float64(optimal_total_bytes))
			fmt.Printf(" > gorilla_total_bytes: %d, gorilla compression ratio: %f, time: %s, throughput: %f MB/s\n",
				gorilla_total_bytes,
				float64(uncompressed_total_bytes)/float64(gorilla_total_bytes),
				gorilla_total_time,
				float64(uncompressed_total_bytes)/(float64(1<<20)*float64(gorilla_total_time)/float64(time.Second)))
			fmt.Printf(" > simple8b_total_bytes: %d, simple8b compression ratio: %f, time: %s, throughput: %f MB/s\n",
				simple8b_total_bytes,
				float64(uncompressed_total_bytes)/float64(simple8b_total_bytes),
				simple8b_total_time,
				float64(uncompressed_total_bytes)/(float64(1<<20)*float64(simple8b_total_time)/float64(time.Second)))
			fmt.Printf(" > bitpacking_total_bytes: %d, bitpacking compression ratio: %f, time: %s, throughput: %f MB/s\n",
				bitpacking_total_bytes,
				float64(uncompressed_total_bytes)/float64(bitpacking_total_bytes),
				bitpacking_total_time,
				float64(uncompressed_total_bytes)/(float64(1<<20)*float64(bitpacking_total_time)/float64(time.Second)))
			fmt.Printf(" > varint_total_bytes: %d, varint compression ratio: %f, time: %s, throughput: %f MB/s\n",
				varint_total_bytes,
				float64(uncompressed_total_bytes)/float64(varint_total_bytes),
				varint_total_time,
				float64(uncompressed_total_bytes)/(float64(1<<20)*float64(varint_total_time)/float64(time.Second)))
			fmt.Printf(" > pfor_total_bytes: %d, pfor compression ratio: %f, time: %s, throughput: %f MB/s\n",
				pfor_total_bytes,
				float64(uncompressed_total_bytes)/float64(pfor_total_bytes),
				pfor_total_time,
				float64(uncompressed_total_bytes)/(float64(1<<20)*float64(pfor_total_time)/float64(time.Second)))
			fmt.Printf(" > simplebits_total_bytes: %d, simplebits compression ratio: %f, time: %s, throughput: %f MB/s\n\n\n\n",
				simplebits_total_bytes,
				float64(uncompressed_total_bytes)/float64(simplebits_total_bytes),
				simplebits_total_time,
				float64(uncompressed_total_bytes)/(float64(1<<20)*float64(simplebits_total_time)/float64(time.Second)))
			fmt.Printf(" > huffman_total_bytes: %d, huffman compression ratio: %f, time: %s, throughput: %f MB/s\n\n\n\n",
				huffman_total_bytes,
				float64(uncompressed_total_bytes)/float64(huffman_total_bytes),
				huffman_total_time,
				float64(uncompressed_total_bytes)/(float64(1<<20)*float64(huffman_total_time)/float64(time.Second)))
		}
	}
}

type DataCounter struct {
	Bits       int
	Proportion float64
	Count      uint64

	AvgLessAndEqualN float64
	AvgEqualN        float64
	AvgGreaterN      float64
}

func DatasetStatistics(src []uint64) ([]DataCounter, int) {
	bitCounters := make([]DataCounter, 64+1)
	maxBits := 0

	// Count the number of value corresponding to every bit width
	for _, v := range src {
		width := chunkenc.BitWidth(v)
		bitCounters[width].Count++
	}

	for bits := 1; bits <= 64; bits++ {
		if bitCounters[bits].Count > 0 {
			bitCounters[bits].Proportion = float64(bitCounters[bits].Count) / float64(len(src))
			maxBits = bits
		}
		bitCounters[bits].Bits = bits
	}

	// Calculate average N for data points less than or equal to certain bits width
	for bits := 1; bits <= maxBits; bits++ {
		totalN := uint64(0)
		countN := uint64(0)

		for i := 0; i < len(src); i++ {
			currN := uint64(0)
			for i < len(src) && chunkenc.BitWidth(src[i]) <= bits {
				currN++
				i++
			}
			if currN > 0 {
				totalN += currN
				countN++
			}
		}

		if countN == 0 {
			bitCounters[bits].AvgLessAndEqualN = 0
		} else {
			bitCounters[bits].AvgLessAndEqualN = float64(totalN) / float64(countN)
		}
	}

	// Calculate average N for data points equal to certain bits width
	for bits := 1; bits <= maxBits; bits++ {
		totalN := uint64(0)
		countN := uint64(0)

		for i := 0; i < len(src); i++ {
			currN := uint64(0)
			for i < len(src) && chunkenc.BitWidth(src[i]) == bits {
				currN++
				i++
			}
			if currN > 0 {
				totalN += currN
				countN++
			}
		}

		if countN == 0 {
			bitCounters[bits].AvgEqualN = 0
		} else {
			bitCounters[bits].AvgEqualN = float64(totalN) / float64(countN)
		}
	}

	// Calculate average N for data points greater than certain bits width
	for bits := 1; bits <= maxBits; bits++ {
		totalN := uint64(0)
		countN := uint64(0)

		for i := 0; i < len(src); i++ {
			currN := uint64(0)
			for i < len(src) && chunkenc.BitWidth(src[i]) > bits {
				currN++
				i++
			}
			if currN > 0 {
				totalN += currN
				countN++
			}
		}

		if countN == 0 {
			bitCounters[bits].AvgGreaterN = 0
		} else {
			bitCounters[bits].AvgGreaterN = float64(totalN) / float64(countN)
		}
	}

	return bitCounters, maxBits
}

func BenchmarkDatasets(b *testing.B) {
	dl := tsdb.NewDatasetLoader()

	// ErrorBound is absolute error bound for compression algorithms.
	ErrorBound := 1e-4
	Datasets := map[string]string{
		"pamapv2": "/home/jzj/Datasets/PAMAP2_Dataset/Protocol/subject101.dat",
		// "ett":         "/home/jzj/Datasets/ETT/ETTh1.csv",
		// "wisdm":       "/home/jzj/Datasets/WISDM_Dataset/raw/phone/accel/data_1600_accel_phone.txt",
		// "electricity": "/home/jzj/Datasets/Electricity/LD2011_2014.txt",
	}

	Timeseries := map[string]int{
		"pamapv2":     10,
		"ett":         0,
		"wisdm":       0,
		"electricity": 0,
	}

	for name, path := range Datasets {
		fmt.Printf("Dataset: %s\n", name)

		switch name {
		case "pamapv2":
			dl.ReadPAMAP2File(path)
		case "ett":
			dl.ReadETTFile(path)
		case "wisdm":
			dl.ReadWISDMFile(path)
		case "electricity":
			dl.ReadElectricityFile(path)
		}
		vals := dl.Samples[Timeseries[name]]

		// data_delta := make([]int64, 0, len(vals))
		data_zigzag := make([]uint64, 0, len(vals))
		curr_val := int64(0)
		for i := 0; i < len(vals); i += 1 {
			var f2i int64
			if vals[i].V >= 0 {
				f2i = int64(vals[i].V/(2*ErrorBound) + 0.5)
			} else {
				f2i = int64(vals[i].V/(2*ErrorBound) - 0.5)
			}
			fdelta := f2i - curr_val
			curr_val = f2i

			// data_delta = append(data_delta, fdelta)

			if fdelta >= 0 {
				fdelta <<= 1
			} else {
				fdelta = -2*fdelta - 1
			}

			data_zigzag = append(data_zigzag, uint64(fdelta))
		}

		dataCounters, maxBits := DatasetStatistics(data_zigzag)

		_, _, stat := chunkenc.EncodeAllWithStatistics(data_zigzag)
		selectors := []chunkenc.BitSelector{
			{Bits: 0, Selector: 0, N: 240},
			{Bits: 0, Selector: 1, N: 120},
			{Bits: 1, Selector: 2, N: 60},
			{Bits: 2, Selector: 3, N: 30},
			{Bits: 3, Selector: 4, N: 20},
			{Bits: 4, Selector: 5, N: 15},
			{Bits: 5, Selector: 6, N: 12},
			{Bits: 6, Selector: 7, N: 10},
			{Bits: 7, Selector: 8, N: 8},
			{Bits: 8, Selector: 9, N: 7},
			{Bits: 10, Selector: 10, N: 6},
			{Bits: 12, Selector: 11, N: 5},
			{Bits: 15, Selector: 12, N: 4},
			{Bits: 20, Selector: 13, N: 3},
			{Bits: 30, Selector: 14, N: 2},
			{Bits: 60, Selector: 15, N: 1},
		}
		fmt.Printf("Simple8b Statistics: \n")
		fmt.Printf("Bits\tSelector\tN\tCount\n")
		for i, count := range stat {
			fmt.Printf("|%d\t|%d\t|%d\t|%d|\n",
				selectors[i].Bits,
				selectors[i].Selector,
				selectors[i].N,
				count)
		}
		fmt.Printf("\n\n")

		fmt.Printf("Bits\tProportion\tCount\tAvgLessAndEqualN\tAvgEqualN\tAvgGreaterN\n")
		for bits := 1; bits <= maxBits; bits++ {
			fmt.Printf("%d\t%f\t%d\t%f\t%f\t%f\n",
				dataCounters[bits].Bits,
				dataCounters[bits].Proportion,
				dataCounters[bits].Count,
				dataCounters[bits].AvgLessAndEqualN,
				dataCounters[bits].AvgEqualN,
				dataCounters[bits].AvgGreaterN)
		}
		fmt.Printf("\n\n")

	}
}

func BenchmarkCompress(b *testing.B) {
	tb := &CompressBenchmark{
		outPath: "/home/jzj/benchout",
		logger:  promslog.New(&promslog.Config{}),
	}
	dl := tsdb.NewDatasetLoader()

	if tb.outPath == "" {
		dir, err := os.MkdirTemp("", "tsdb_bench")
		if err != nil {
			return
		}
		tb.outPath = dir
		tb.cleanup = true
	}
	if err := os.RemoveAll(tb.outPath); err != nil {
		return
	}
	if err := os.MkdirAll(tb.outPath, 0o777); err != nil {
		return
	}

	dir := filepath.Join(tb.outPath, "storage")
	chunkenc.DefaultCtype = chunkenc.Huffman
	chunkenc.DefaultErrbound = 1e-2

	st, err := tsdb.Open(dir, tb.logger, nil, &tsdb.Options{
		RetentionDuration:    int64(3650 * 24 * time.Hour / time.Millisecond),
		MinBlockDuration:     int64(2 * time.Hour / time.Millisecond),
		MaxBlockDuration:     int64(162 * time.Hour / time.Millisecond),
		OutOfOrderTimeWindow: int64(7200 * timeDelta),
		SamplesPerChunk:      1000,
		OutOfOrderCapMax:     255,
		WALSegmentSize:       0,
		ErrorBound:           chunkenc.DefaultErrbound,
	}, tsdb.NewDBStats())
	if err != nil {
		return
	}
	st.DisableCompactions()
	tb.storage = st
	if err := dl.ReadDataset("pamapv2", "/home/jzj/Datasets/PAMAP2_Dataset_mini"); err != nil {
		return
	}

	// tb.generateOOO()

	valid_lines := len(dl.Samples[0])
	for i := 0; i < len(dl.Samples); i += 1 {
		valid_lines = max(valid_lines, len(dl.Samples[i]))
	}

	var total uint64

	// timeStart := time.Now()

	dur, err := measureTime("ingestScrapes", func() error {
		if err := tb.startProfiling(); err != nil {
			return err
		}

		for line := 0; line < valid_lines; line += 1 {
			app := tb.storage.Appender(context.TODO())
			for lbs := 0; lbs < len(dl.Samples); lbs += 1 {
				if line >= len(dl.Samples[lbs]) {
					continue
				}
				var ref storage.SeriesRef
				if dl.Scrape[lbs].Ref != nil {
					ref = *dl.Scrape[lbs].Ref
				}

				ref, err := app.Append(ref, dl.Scrape[lbs].Labels, dl.Samples[lbs][line].T, dl.Samples[lbs][line].V)
				if err != nil {
					panic(err)
				}

				if dl.Scrape[lbs].Ref == nil {
					dl.Scrape[lbs].Ref = &ref
				}
			}
			if err := app.Commit(); err != nil {
				return err
			}
			total += uint64(len(dl.Samples))
		}
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return
	}

	fmt.Println(" > total samples:", total)
	fmt.Println(" > samples/sec:", float64(total)/dur.Seconds())

	// fmt.Println(" > total OOO samples:", tsdb.OOOSamplesTotal_test)
	// fmt.Println(" > total OOO chunks:", tsdb.OOOChunksTotal_test)
	// fmt.Println(" > total OOO chunk size:", tsdb.OOOCompressedSize_test)
	// fmt.Println(" > total OOO chunk timestamp size:", tsdb.OOOChunksTimestamp_test)

	if _, err = measureTime("stopStorage", func() error {
		if err := tb.storage.Close(); err != nil {
			return err
		}

		return tb.stopProfiling()
	}); err != nil {
		return
	}

	// time.Sleep(120 * time.Second)

	tb.storage, _ = tsdb.Open(dir, tb.logger, nil, &tsdb.Options{
		RetentionDuration:    int64(3650 * 24 * time.Hour / time.Millisecond),
		MinBlockDuration:     int64(2 * time.Hour / time.Millisecond),
		MaxBlockDuration:     int64(162 * time.Hour / time.Millisecond),
		OutOfOrderTimeWindow: int64(7200 * timeDelta),
		SamplesPerChunk:      1000,
		OutOfOrderCapMax:     255,
		WALSegmentSize:       0,
		ErrorBound:           chunkenc.DefaultErrbound,
	}, tsdb.NewDBStats())

	// PAMAPv2/Protocol/subject101.dat中第11列（3D-gyroscope data (rad/s) ）为例
	m1, err := labels.NewMatcher(labels.MatchEqual, "FileID", "0")
	if err != nil {
		return
	}
	m2, err := labels.NewMatcher(labels.MatchEqual, "CaseID", "10")
	if err != nil {
		return
	}

	// 确定查询数据范围
	// Determine the query time range from the loaded dataset.
	minTime := int64(360000 * timeDelta)
	maxTime := int64(370000 * timeDelta)

	if _, err := measureTime("selectFile", func() error {
		if err, _ := tb.selectTestThroughput(minTime, maxTime, m1, m2); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return
	}
}

func BenchmarkSelect(b *testing.B) {
	tb := &CompressBenchmark{
		outPath: "/home/jzj/benchout",
		logger:  promslog.New(&promslog.Config{}),
	}

	dir := filepath.Join(tb.outPath, "storage")

	st, err := tsdb.Open(dir, tb.logger, nil, &tsdb.Options{
		RetentionDuration:    int64(3650 * 24 * time.Hour / time.Millisecond),
		MinBlockDuration:     int64(2 * time.Hour / time.Millisecond),
		MaxBlockDuration:     int64(162 * time.Hour / time.Millisecond),
		OutOfOrderTimeWindow: int64(7200 * timeDelta),
		SamplesPerChunk:      1000,
		OutOfOrderCapMax:     255,
		WALSegmentSize:       -1,
	}, tsdb.NewDBStats())
	if err != nil {
		b.Fatalf("failed to open tsdb: %v", err)
	}
	defer st.Close()

	tb.storage = st

	m1, err := labels.NewMatcher(labels.MatchEqual, "FileID", "0")
	if err != nil {
		b.Fatalf("failed to create matcher: %v", err)
	}
	m2, err := labels.NewMatcher(labels.MatchEqual, "CaseID", "10")
	if err != nil {
		b.Fatalf("failed to create matcher: %v", err)
	}

	// Determine the query time range from the loaded dataset.
	minTime := int64(0 * timeDelta)
	maxTime := int64(376400 * timeDelta)
	queryDuration := int64(time.Hour.Milliseconds())

	if maxTime-minTime <= queryDuration {
		b.Fatalf("total time range of data is smaller than query duration")
	}

	latencies := make([]time.Duration, 0, b.N)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Generate a random start time for the query.
		randomStartOffset := time.Duration(rand.Intn(int(maxTime-minTime-queryDuration))) * time.Millisecond
		tMin := minTime + randomStartOffset.Milliseconds()
		tMax := tMin + queryDuration

		start := time.Now()
		err, _ := tb.selectTestThroughput(tMin, tMax, m1, m2)
		latency := time.Since(start)

		if err != nil {
			// Don't fail the benchmark, but log the error.
			b.Logf("query failed: %v", err)
		}
		latencies = append(latencies, latency)
	}

	b.StopTimer()

	// Calculate and report statistics.
	if len(latencies) > 0 {
		slices.Sort(latencies)
		p99Index := int(float64(len(latencies)) * 0.99)
		p99 := latencies[p99Index]
		qps := float64(len(latencies)) / b.Elapsed().Seconds()

		var totalLatency time.Duration
		for _, l := range latencies {
			totalLatency += l
		}
		avgLatency := totalLatency / time.Duration(len(latencies))

		fmt.Printf("\n--- Select Benchmark Results ---\n")
		fmt.Printf("Total Queries: %d\n", len(latencies))
		fmt.Printf("QPS: %.2f\n", qps)
		fmt.Printf("P99 Latency: %s\n", p99)
		fmt.Printf("Average Latency: %s\n", avgLatency)
		fmt.Printf("Min Latency: %s\n", latencies[0])
		fmt.Printf("Max Latency: %s\n", latencies[len(latencies)-1])
		fmt.Printf("---------------------------------\n")
	}
}

func TestGenerateBucket(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		min, max         int
		start, end, step int
	}{
		{
			min:   101,
			max:   141,
			start: 100,
			end:   150,
			step:  10,
		},
	}

	for _, tc := range tcs {
		start, end, step := generateBucket(tc.min, tc.max)

		require.Equal(t, tc.start, start)
		require.Equal(t, tc.end, end)
		require.Equal(t, tc.step, step)
	}
}

// getDumpedSamples dumps samples and returns them.
func getDumpedSamples(t *testing.T, databasePath, sandboxDirRoot string, mint, maxt int64, match []string, formatter SeriesSetFormatter) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := dumpSamples(
		context.Background(),
		databasePath,
		sandboxDirRoot,
		mint,
		maxt,
		match,
		formatter,
	)
	require.NoError(t, err)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func normalizeNewLine(b []byte) []byte {
	if strings.Contains(runtime.GOOS, "windows") {
		// We use "/n" while dumping on windows as well.
		return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	}
	return b
}

func TestTSDBDump(t *testing.T) {
	storage := promqltest.LoadedStorage(t, `
		load 1m
			metric{foo="bar", baz="abc"} 1 2 3 4 5
			heavy_metric{foo="bar"} 5 4 3 2 1
			heavy_metric{foo="foo"} 5 4 3 2 1
	`)
	t.Cleanup(func() { storage.Close() })

	tests := []struct {
		name           string
		mint           int64
		maxt           int64
		sandboxDirRoot string
		match          []string
		expectedDump   string
	}{
		{
			name:         "default match",
			mint:         math.MinInt64,
			maxt:         math.MaxInt64,
			match:        []string{"{__name__=~'(?s:.*)'}"},
			expectedDump: "testdata/dump-test-1.prom",
		},
		{
			name:           "default match with sandbox dir root set",
			mint:           math.MinInt64,
			maxt:           math.MaxInt64,
			sandboxDirRoot: t.TempDir(),
			match:          []string{"{__name__=~'(?s:.*)'}"},
			expectedDump:   "testdata/dump-test-1.prom",
		},
		{
			name:         "same matcher twice",
			mint:         math.MinInt64,
			maxt:         math.MaxInt64,
			match:        []string{"{foo=~'.+'}", "{foo=~'.+'}"},
			expectedDump: "testdata/dump-test-1.prom",
		},
		{
			name:         "no duplication",
			mint:         math.MinInt64,
			maxt:         math.MaxInt64,
			match:        []string{"{__name__=~'(?s:.*)'}", "{baz='abc'}"},
			expectedDump: "testdata/dump-test-1.prom",
		},
		{
			name:         "well merged",
			mint:         math.MinInt64,
			maxt:         math.MaxInt64,
			match:        []string{"{__name__='heavy_metric'}", "{baz='abc'}"},
			expectedDump: "testdata/dump-test-1.prom",
		},
		{
			name:         "multi matchers",
			mint:         math.MinInt64,
			maxt:         math.MaxInt64,
			match:        []string{"{__name__='heavy_metric',foo='foo'}", "{__name__='metric'}"},
			expectedDump: "testdata/dump-test-2.prom",
		},
		{
			name:         "with reduced mint and maxt",
			mint:         int64(60000),
			maxt:         int64(120000),
			match:        []string{"{__name__='metric'}"},
			expectedDump: "testdata/dump-test-3.prom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dumpedMetrics := getDumpedSamples(t, storage.Dir(), tt.sandboxDirRoot, tt.mint, tt.maxt, tt.match, formatSeriesSet)
			expectedMetrics, err := os.ReadFile(tt.expectedDump)
			require.NoError(t, err)
			expectedMetrics = normalizeNewLine(expectedMetrics)
			// Sort both, because Prometheus does not guarantee the output order.
			require.Equal(t, sortLines(string(expectedMetrics)), sortLines(dumpedMetrics))
		})
	}
}

func sortLines(buf string) string {
	lines := strings.Split(buf, "\n")
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

func TestTSDBDumpOpenMetrics(t *testing.T) {
	storage := promqltest.LoadedStorage(t, `
		load 1m
			my_counter{foo="bar", baz="abc"} 1 2 3 4 5
			my_gauge{bar="foo", abc="baz"} 9 8 0 4 7
	`)
	t.Cleanup(func() { storage.Close() })

	tests := []struct {
		name           string
		sandboxDirRoot string
	}{
		{
			name: "default match",
		},
		{
			name:           "default match with sandbox dir root set",
			sandboxDirRoot: t.TempDir(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expectedMetrics, err := os.ReadFile("testdata/dump-openmetrics-test.prom")
			require.NoError(t, err)
			expectedMetrics = normalizeNewLine(expectedMetrics)
			dumpedMetrics := getDumpedSamples(t, storage.Dir(), tt.sandboxDirRoot, math.MinInt64, math.MaxInt64, []string{"{__name__=~'(?s:.*)'}"}, formatSeriesSetOpenMetrics)
			require.Equal(t, sortLines(string(expectedMetrics)), sortLines(dumpedMetrics))
		})
	}
}

func TestTSDBDumpOpenMetricsRoundTrip(t *testing.T) {
	initialMetrics, err := os.ReadFile("testdata/dump-openmetrics-roundtrip-test.prom")
	require.NoError(t, err)
	initialMetrics = normalizeNewLine(initialMetrics)

	dbDir := t.TempDir()
	// Import samples from OM format
	err = backfill(5000, initialMetrics, dbDir, false, false, 2*time.Hour, map[string]string{})
	require.NoError(t, err)
	db, err := tsdb.Open(dbDir, nil, nil, tsdb.DefaultOptions(), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	// Dump the blocks into OM format
	dumpedMetrics := getDumpedSamples(t, dbDir, "", math.MinInt64, math.MaxInt64, []string{"{__name__=~'(?s:.*)'}"}, formatSeriesSetOpenMetrics)

	// Should get back the initial metrics.
	require.Equal(t, string(initialMetrics), dumpedMetrics)
}
