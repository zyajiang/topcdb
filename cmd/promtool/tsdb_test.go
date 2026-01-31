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
	tb := &compressBenchmark{
		logger: promslog.New(&promslog.Config{}),
	}

	// ErrorBound is absolute error bound for compression algorithms.
	ErrorBound := 0.0001
	// The Whole dataset will be divided into chunks of ChunkSize for compression.
	ChunkSize := 1000
	Datasets := map[string]string{
		"pamapv2":           "/home/jzj/Datasets/PAMAP2_Dataset",
		"uci_gas":           "/home/jzj/Datasets/UCI_GAS",
		"ucr":               "/home/jzj/Datasets/UCRAchive_2018",
		"ett":               "/home/jzj/Datasets/ETT",
		"household_voltage": "/home/jzj/Datasets/Household_Voltage",
	}

	for name, path := range Datasets {
		fmt.Printf("Dataset: %s\n", name)

		tb.samples = make([][]tvpair, 0, 8)
		if err := tb.ReadDataset(name, path); err != nil {
			return
		}

		var uncompressed_total_bytes int
		var timestamp_total_bytes int
		var sz_total_bytes, most_total_bytes, machete_total_bytes, gorilla_total_bytes int
		var cl_total_bytes, simplebits_total_bytes int

		for _, vals := range tb.samples {
			currChunkSize := min(ChunkSize, len(vals))

			for batch := 0; batch*currChunkSize < len(vals); batch += 1 {
				uncompressed_data := make([]float64, 0, currChunkSize)

				// Gorilla
				gorilla_chk := chunkenc.NewXORChunk()
				gorilla_app, _ := gorilla_chk.Appender()

				// Compactable Lossy
				cl_chk := chunkenc.NewCLChunk()
				cl_app, _ := cl_chk.Appender()
				cl_app.(*chunkenc.CLAppender).SetCompressType(chunkenc.QSimple8b)
				cl_app.(*chunkenc.CLAppender).SetErrorBound(ErrorBound)
				for i := batch * currChunkSize; i < (batch+1)*currChunkSize && i < len(vals); i += 1 {
					uncompressed_data = append(uncompressed_data, vals[i].v)
					gorilla_app.Append(int64(i), vals[i].v)
					cl_app.Append(int64(i), vals[i].v)
				}
				cl_app.(*chunkenc.CLAppender).Compact()

				// Simplebits
				bitCounts, maxBits := chunkenc.BitStatistics(cl_app.(*chunkenc.CLAppender).Simplebits_buffer)
				simplebits_chk := chunkenc.PackingAll(cl_app.(*chunkenc.CLAppender).Simplebits_buffer, chunkenc.BitSelectors(bitCounts, chunkenc.Simplebits_MaxSegmentsNum, maxBits))

				// SZ, MOST, Machete
				var outSize uint64
				comp_sz := chunkenc.SZ_Compress(1, uncompressed_data, &outSize, 0, ErrorBound, 0, 0, 0, 0, 0, 0, uint64(ChunkSize))
				comp_most := chunkenc.MOST_Compress(uncompressed_data, ErrorBound, 5)
				comp_machete := chunkenc.Machete_Compress(uncompressed_data, int64(len(uncompressed_data)), ErrorBound)

				// Timestamp bytes are the same for all algorithms
				timestamp_total_bytes = cl_app.(*chunkenc.CLAppender).TimestampSize()

				// Total bytes for each algorithm
				gorilla_total_bytes += len(gorilla_chk.Bytes()) - timestamp_total_bytes
				cl_total_bytes += len(cl_chk.Bytes()) - timestamp_total_bytes
				simplebits_total_bytes += simplebits_chk.Len()
				sz_total_bytes += len(comp_sz)
				most_total_bytes += len(comp_most)
				machete_total_bytes += len(comp_machete)
				uncompressed_total_bytes += currChunkSize * 8
			}
		}

		fmt.Printf(" > uncompressed_total_bytes: %d\n", uncompressed_total_bytes)
		fmt.Printf(" > sz_total_bytes: %d, sz compression ratio: %f\n", sz_total_bytes, float64(uncompressed_total_bytes)/float64(sz_total_bytes))
		fmt.Printf(" > most_total_bytes: %d, most compression ratio: %f\n", most_total_bytes, float64(uncompressed_total_bytes)/float64(most_total_bytes))
		fmt.Printf(" > machete_total_bytes: %d, machete compression ratio: %f\n", machete_total_bytes, float64(uncompressed_total_bytes)/float64(machete_total_bytes))
		fmt.Printf(" > gorilla_total_bytes: %d, gorilla compression ratio: %f\n", gorilla_total_bytes, float64(uncompressed_total_bytes)/float64(gorilla_total_bytes))
		fmt.Printf(" > cl_total_bytes: %d, cl compression ratio: %f\n", cl_total_bytes, float64(uncompressed_total_bytes)/float64(cl_total_bytes))
		fmt.Printf(" > simplebits_total_bytes: %d, simplebits compression ratio: %f\n\n\n", simplebits_total_bytes, float64(uncompressed_total_bytes)/float64(simplebits_total_bytes))
	}
}

func BenchmarkCompress(b *testing.B) {
	tb := &compressBenchmark{
		outPath: "/home/jzj/benchout",
		logger:  promslog.New(&promslog.Config{}),
	}
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
	chunkenc.DefaultCtype = chunkenc.QSimple8b
	chunkenc.DefaultErrbound = 0.01

	st, err := tsdb.Open(dir, tb.logger, nil, &tsdb.Options{
		RetentionDuration:    int64(3650 * 24 * time.Hour / time.Millisecond),
		MinBlockDuration:     int64(2 * time.Hour / time.Millisecond),
		MaxBlockDuration:     int64(162 * time.Hour / time.Millisecond),
		OutOfOrderTimeWindow: int64(7200 * timeDelta),
		SamplesPerChunk:      1000,
		OutOfOrderCapMax:     255,
		WALSegmentSize:       -1,
		ErrorBound:           0.01,
	}, tsdb.NewDBStats())
	if err != nil {
		return
	}
	// st.DisableCompactions()
	tb.storage = st
	tb.samples = make([][]tvpair, 0, 8)
	tb.scrape = make([]*lb, 0, len(tb.samples))

	if err := tb.ReadDataset("household_voltage", "/home/jzj/Datasets/HouseholdVoltage"); err != nil {
		return
	}

	// tb.generateOOO()

	valid_lines := len(tb.samples[0])
	for i := 0; i < len(tb.samples); i += 1 {
		valid_lines = max(valid_lines, len(tb.samples[i]))
	}

	var total uint64

	dur, err := measureTime("ingestScrapes", func() error {
		if err := tb.startProfiling(); err != nil {
			return err
		}

		for line := 0; line < valid_lines; line += 1 {
			app := tb.storage.Appender(context.TODO())
			for lbs := 0; lbs < len(tb.samples); lbs += 1 {
				if line >= len(tb.samples[lbs]) {
					continue
				}
				var ref storage.SeriesRef
				if tb.scrape[lbs].ref != nil {
					ref = *tb.scrape[lbs].ref
				}

				ref, err := app.Append(ref, tb.scrape[lbs].labels, tb.samples[lbs][line].t, tb.samples[lbs][line].v)
				if err != nil {
					panic(err)
				}

				if tb.scrape[lbs].ref == nil {
					tb.scrape[lbs].ref = &ref
				}
			}
			if err := app.Commit(); err != nil {
				return err
			}
			total += uint64(len(tb.samples))
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

	time.Sleep(10 * time.Second)

	m1, err := labels.NewMatcher(labels.MatchEqual, "FileID", "0")
	if err != nil {
		return
	}
	m2, err := labels.NewMatcher(labels.MatchEqual, "CaseID", "0")
	if err != nil {
		return
	}

	if err := tb.selectFile(m1, m2); err != nil {
		return
	}

	if _, err = measureTime("stopStorage", func() error {
		if err := tb.storage.Close(); err != nil {
			return err
		}

		return tb.stopProfiling()
	}); err != nil {
		return
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
