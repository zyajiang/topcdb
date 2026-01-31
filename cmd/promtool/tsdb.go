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
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/alecthomas/units"
	"go.uber.org/atomic"

	"github.com/prometheus/common/promslog"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/tsdb/chunks"
	tsdb_errors "github.com/prometheus/prometheus/tsdb/errors"
	"github.com/prometheus/prometheus/tsdb/fileutil"
	"github.com/prometheus/prometheus/tsdb/index"
)

const timeDelta = 1000

type lb struct {
	labels labels.Labels
	ref    *storage.SeriesRef
}

type tvpair struct {
	t int64
	v float64
}

type compressBenchmark struct {
	outPath string

	samples [][]tvpair
	scrape  []*lb

	cleanup bool
	storage *tsdb.DB

	cpuprof   *os.File
	memprof   *os.File
	blockprof *os.File
	mtxprof   *os.File
	logger    *slog.Logger
}

func benchmarkCompress(outPath, dataset, path, algo string, errbound float64) error {
	chunkenc.DefaultErrbound = errbound

	switch algo {
	case "qsimple8b":
		chunkenc.DefaultCtype = chunkenc.QSimple8b
	case "sz":
		chunkenc.DefaultCtype = chunkenc.SZ3
	case "machete":
		chunkenc.DefaultCtype = chunkenc.Machete
	case "most":
		chunkenc.DefaultCtype = chunkenc.MOST
	}

	tb := &compressBenchmark{
		outPath: outPath,
		logger:  promslog.New(&promslog.Config{}),
	}

	if tb.outPath == "" {
		dir, err := os.MkdirTemp("", "topcdb_bench")
		if err != nil {
			return err
		}
		tb.outPath = dir
		tb.cleanup = true
	}
	if err := os.RemoveAll(tb.outPath); err != nil {
		return err
	}
	if err := os.MkdirAll(tb.outPath, 0o777); err != nil {
		return err
	}

	dir := filepath.Join(tb.outPath, "storage")

	st, err := tsdb.Open(dir, tb.logger, nil, &tsdb.Options{
		RetentionDuration:    int64(3650 * 24 * time.Hour / time.Millisecond),
		MinBlockDuration:     int64(2 * time.Hour / time.Millisecond),
		MaxBlockDuration:     int64(162 * time.Hour / time.Millisecond),
		OutOfOrderTimeWindow: int64(7200 * timeDelta),
		SamplesPerChunk:      1000,
		WALSegmentSize:       -1,
		ErrorBound:           0.01,
	}, tsdb.NewDBStats())
	if err != nil {
		return err
	}
	// st.DisableCompactions()
	tb.storage = st
	tb.samples = make([][]tvpair, 0, 8)
	tb.scrape = make([]*lb, 0, len(tb.samples))

	if tb.ReadDataset(dataset, path) != nil {
		return err
	}

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
		return err
	}

	fmt.Println(" > total samples:", total)
	fmt.Println(" > samples/sec:", float64(total)/dur.Seconds())

	time.Sleep(120 * time.Second)

	m1, err := labels.NewMatcher(labels.MatchEqual, "FileID", "0")
	if err != nil {
		return err
	}
	m2, err := labels.NewMatcher(labels.MatchEqual, "CaseID", "3")
	if err != nil {
		return err
	}

	if err := tb.selectFile(m1, m2); err != nil {
		return err
	}

	if _, err = measureTime("stopStorage", func() error {
		if err := tb.storage.Close(); err != nil {
			return err
		}

		return tb.stopProfiling()
	}); err != nil {
		return err
	}
	return nil
}

func (b *compressBenchmark) ReadDataset(dataset, path string) error {
	num_labels := 1

	switch dataset {
	case "pamapv2":
		// list all files in the directory
		listFiles := func(dir string) ([]string, error) {
			files := []string{}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".dat") {
					files = append(files, filepath.Join(dir, entry.Name()))
				}
			}
			return files, nil
		}

		for _, sub := range []string{"Protocol", "Optional"} {
			files, err := listFiles(filepath.Join(path, sub))
			if err != nil {
				return err
			}
			for _, file := range files {
				if err := b.readPAMAP2File(file); err != nil {
					return err
				}
			}
		}

		num_labels = 39
	case "uci_gas":
		for _, sub := range []string{"ethylene_CO.txt", "ethylene_methane.txt"} {
			if err := b.readUCI_GASFile(filepath.Join(path, sub)); err != nil {
				return err
			}
		}

		num_labels = 16
	case "ucr":
		DIR, err := os.Open(path)
		if err != nil {
			log.Fatal(err)
		}
		subdirs, err := DIR.ReadDir(-1)
		DIR.Close()
		if err != nil {
			log.Fatal(err)
		}

		re, err := regexp.Compile(".*tsv")
		if err != nil {
			log.Fatal(err)
		}

		for _, subdir := range subdirs {
			dir, err := os.Open(filepath.Join(path, subdir.Name()))
			if err != nil {
				log.Fatal(err)
			}
			files, err := dir.ReadDir(-1)
			dir.Close()
			if err != nil {
				log.Fatal(err)
			}
			for _, file := range files {
				matched := re.MatchString(file.Name())
				if !matched {
					continue
				}
				if b.readUCRFile(filepath.Join(path, subdir.Name(), file.Name())) != nil {
					return err
				}
			}
		}

		num_labels = len(b.samples)
	case "ett":
		for _, sub := range []string{"ETTh1.csv", "ETTh2.csv", "ETTm1.csv", "ETTm2.csv"} {
			if err := b.readETTFile(filepath.Join(path, sub)); err != nil {
				return err
			}
		}

		num_labels = 7
	case "household_voltage":
		if err := b.readHouseholdVoltageFile(filepath.Join(path, "household_power_consumption.txt")); err != nil {
			return err
		}

		num_labels = len(b.samples)
	}

	for i := 0; i < len(b.samples); i += 1 {
		b.scrape = append(b.scrape, &lb{
			labels: labels.New(
				labels.Label{Name: "FileID", Value: strconv.Itoa(i / num_labels)},
				labels.Label{Name: "CaseID", Value: strconv.Itoa(i)},
			),
		})
	}

	return nil
}

type oooInput struct {
	arrive  []int64
	samples []tvpair
}

func (o *oooInput) Len() int {
	return len(o.arrive)
}

func (o *oooInput) Less(i, j int) bool {
	return o.arrive[i] < o.arrive[j]
}

func (o *oooInput) Swap(i, j int) {
	o.arrive[i], o.arrive[j] = o.arrive[j], o.arrive[i]
	o.samples[i], o.samples[j] = o.samples[j], o.samples[i]
}

func (b *compressBenchmark) generateOOO() {
	source := rand.NewSource(0)
	r := rand.New(source)
	oooRatio := float64(0.2)
	for i := 0; i < len(b.samples); i += 1 {
		arrive := make([]int64, len(b.samples[i]))
		for j := 0; j < len(b.samples[i]); j += 1 {
			coin := r.Float64()
			arrive[j] = b.samples[i][j].t
			if coin < oooRatio {
				tDelta := int64((1200 * timeDelta * coin / oooRatio))
				arrive[j] = b.samples[i][j].t + tDelta
			}
		}
		sort.Sort(&oooInput{arrive: arrive, samples: b.samples[i]})
	}
}

func (b *compressBenchmark) selectFile(matchers ...*labels.Matcher) error {
	qblocks := make([]storage.Querier, 0, len(b.storage.Blocks()))

	tMin := int64(0 * timeDelta)
	tMax := int64(100000 * timeDelta)

	for _, blk := range b.storage.Blocks() {
		q, err := tsdb.NewBlockQuerier(blk, tMin, tMax)
		if err != nil {
			return err
		}
		qblocks = append(qblocks, q)
	}

	sq := storage.NewMergeQuerier(qblocks, nil, storage.ChainedSeriesMerge)
	defer sq.Close()

	total := 0
	errnum := 0

	m2, err := strconv.Atoi(matchers[1].Value)
	if err != nil {
		return err
	}
	col := m2
	samples := make(map[int64]float64)
	for _, sample := range b.samples[col] {
		if sample.t >= tMin && sample.t <= tMax {
			samples[sample.t] = sample.v
		}
	}

	ss := sq.Select(context.Background(), false, nil, matchers...)
	var it chunkenc.Iterator
	for ss.Next() {
		series := ss.At()
		it = series.Iterator(it)
		for it.Next() != chunkenc.ValNone {
			t, v := it.At()
			total += 1
			if math.Abs(v-samples[t]) > (1+0.05)*chunkenc.DefaultErrbound {
				fmt.Printf("Precision Error > (%d,%f) (%f)\n", t, v, samples[t])
				errnum += 1
			}
		}
	}
	fmt.Println("\n > total selected samples:", total)
	fmt.Println("\n > total wrong samples:", errnum)
	return nil
}

func (b *compressBenchmark) readHouseholdVoltageFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lbs := len(b.samples)
	for j := 0; j < 7; j++ {
		b.samples = append(b.samples, make([]tvpair, 0, 8))
	}
	row := 0
	val := float64(0)
	scanner.Scan()

	data_error := 0

	for scanner.Scan() {
		row++
		line := strings.Split(scanner.Text(), ";")
		for k := 2; k <= 8; k++ {
			val, err = strconv.ParseFloat(line[k], 64)
			if err != nil {
				data_error++
				continue
			}
			b.samples[lbs+k-2] = append(b.samples[lbs+k-2], tvpair{int64(row * timeDelta), val})
		}
	}

	fmt.Printf("ReadPowerConsumptionFile:: data convertion failed: %d\n", data_error)
	return nil
}

func (b *compressBenchmark) readETTFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		panic(err)
	}

	scanner := bufio.NewScanner(f)
	lbs := len(b.samples)
	for j := 0; j < 7; j++ {
		b.samples = append(b.samples, make([]tvpair, 0, 8))
	}
	row := 0
	val := float64(0)
	scanner.Scan()

	data_error := 0

	for scanner.Scan() {
		row++
		line := strings.Split(scanner.Text(), ",")
		for k := 1; k <= 7; k++ {
			val, err = strconv.ParseFloat(line[k], 64)
			if err != nil {
				data_error++
				continue
			}
			b.samples[lbs+k-1] = append(b.samples[lbs+k-1], tvpair{int64(row * timeDelta), val})
		}
	}

	fmt.Printf("ReadETTFile:: data convertion failed: %d\n", data_error)
	return nil
}

func (b *compressBenchmark) readUCI_GASFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		panic(err)
	}

	scanner := bufio.NewScanner(f)
	lbs := len(b.samples)
	for j := 0; j < 16; j++ {
		b.samples = append(b.samples, make([]tvpair, 0, 8))
	}

	row := 0
	scanner.Scan()
	data_error := 0

	for scanner.Scan() {
		row++
		line := strings.Fields(scanner.Text())
		for j := 3; j < len(line); j++ {
			val, err := strconv.ParseFloat(line[j], 64)
			if err != nil {
				data_error++
				continue
			}
			b.samples[lbs+j-3] = append(b.samples[lbs+j-3], tvpair{int64(row * timeDelta), val})
		}
	}

	fmt.Printf("ReadUCI_GASFile:: data convertion error: %d\n", data_error)
	return nil
}

func (b *compressBenchmark) readUCRFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		panic(err)
	}

	scanner := bufio.NewScanner(f)
	row := len(b.samples)
	data_error := 0

	for scanner.Scan() {
		line := strings.Split(scanner.Text(), "\t")
		b.samples = append(b.samples, make([]tvpair, 0, 8))

		for j := 0; j < len(line); j++ {
			val, err := strconv.ParseFloat(line[j], 64)
			if err != nil {
				data_error++
				continue
			}
			b.samples[row] = append(b.samples[row], tvpair{int64(j * timeDelta), val})
		}
		row++
	}
	fmt.Printf("ReadUCRFile:: data convertion error: %d\n", data_error)
	return nil
}

func (b *compressBenchmark) readPAMAP2File(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for j := 0; j < 39; j++ {
		b.samples = append(b.samples, make([]tvpair, 0, 8))
	}

	row, data_error := 0, 0
	for scanner.Scan() {
		row++
		line := strings.Split(scanner.Text(), " ")
		itr := len(b.samples) - 39
		for k := 0; k < 3; k++ {
			for j := 3 + 17*k; j <= 15+17*k; j++ {
				val, err := strconv.ParseFloat(line[j], 64)
				if err != nil || line[j] == "NaN" {
					data_error++
					continue
				}
				b.samples[itr] = append(b.samples[itr], tvpair{int64(row * timeDelta), val})
				itr++
			}
		}
	}

	fmt.Printf("ReadPAMAP2File:: data convertion error: %d\n", data_error)
	return nil
}

func (b *compressBenchmark) startProfiling() error {
	var err error

	// Start CPU profiling.
	b.cpuprof, err = os.Create(filepath.Join(b.outPath, "cpu.prof"))
	if err != nil {
		return fmt.Errorf("bench: could not create cpu profile: %w", err)
	}
	if err := pprof.StartCPUProfile(b.cpuprof); err != nil {
		return fmt.Errorf("bench: could not start CPU profile: %w", err)
	}

	// Start memory profiling.
	b.memprof, err = os.Create(filepath.Join(b.outPath, "mem.prof"))
	if err != nil {
		return fmt.Errorf("bench: could not create memory profile: %w", err)
	}
	runtime.MemProfileRate = 64 * 1024

	// Start fatal profiling.
	b.blockprof, err = os.Create(filepath.Join(b.outPath, "block.prof"))
	if err != nil {
		return fmt.Errorf("bench: could not create block profile: %w", err)
	}
	runtime.SetBlockProfileRate(20)

	b.mtxprof, err = os.Create(filepath.Join(b.outPath, "mutex.prof"))
	if err != nil {
		return fmt.Errorf("bench: could not create mutex profile: %w", err)
	}
	runtime.SetMutexProfileFraction(20)
	return nil
}

func (b *compressBenchmark) stopProfiling() error {
	if b.cpuprof != nil {
		pprof.StopCPUProfile()
		b.cpuprof.Close()
		b.cpuprof = nil
	}
	if b.memprof != nil {
		if err := pprof.Lookup("heap").WriteTo(b.memprof, 0); err != nil {
			return fmt.Errorf("error writing mem profile: %w", err)
		}
		b.memprof.Close()
		b.memprof = nil
	}
	if b.blockprof != nil {
		if err := pprof.Lookup("block").WriteTo(b.blockprof, 0); err != nil {
			return fmt.Errorf("error writing block profile: %w", err)
		}
		b.blockprof.Close()
		b.blockprof = nil
		runtime.SetBlockProfileRate(0)
	}
	if b.mtxprof != nil {
		if err := pprof.Lookup("mutex").WriteTo(b.mtxprof, 0); err != nil {
			return fmt.Errorf("error writing mutex profile: %w", err)
		}
		b.mtxprof.Close()
		b.mtxprof = nil
		runtime.SetMutexProfileFraction(0)
	}
	return nil
}

type writeBenchmark struct {
	outPath     string
	samplesFile string
	cleanup     bool
	numMetrics  int

	storage *tsdb.DB

	cpuprof   *os.File
	memprof   *os.File
	blockprof *os.File
	mtxprof   *os.File
	logger    *slog.Logger
}

func benchmarkWrite(outPath, samplesFile string, numMetrics, numScrapes int) error {
	b := &writeBenchmark{
		outPath:     outPath,
		samplesFile: samplesFile,
		numMetrics:  numMetrics,
		logger:      promslog.New(&promslog.Config{}),
	}
	if b.outPath == "" {
		dir, err := os.MkdirTemp("", "tsdb_bench")
		if err != nil {
			return err
		}
		b.outPath = dir
		b.cleanup = true
	}
	if err := os.RemoveAll(b.outPath); err != nil {
		return err
	}
	if err := os.MkdirAll(b.outPath, 0o777); err != nil {
		return err
	}

	dir := filepath.Join(b.outPath, "storage")

	st, err := tsdb.Open(dir, b.logger, nil, &tsdb.Options{
		RetentionDuration: int64(15 * 24 * time.Hour / time.Millisecond),
		MinBlockDuration:  int64(2 * time.Hour / time.Millisecond),
	}, tsdb.NewDBStats())
	if err != nil {
		return err
	}
	st.DisableCompactions()
	b.storage = st

	var lbs []labels.Labels

	if _, err = measureTime("readData", func() error {
		f, err := os.Open(b.samplesFile)
		if err != nil {
			return err
		}
		defer f.Close()

		lbs, err = readPrometheusLabels(f, b.numMetrics)
		if err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	var total uint64

	dur, err := measureTime("ingestScrapes", func() error {
		if err := b.startProfiling(); err != nil {
			return err
		}
		total, err = b.ingestScrapes(lbs, numScrapes)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Println(" > total samples:", total)
	fmt.Println(" > samples/sec:", float64(total)/dur.Seconds())

	if _, err = measureTime("stopStorage", func() error {
		if err := b.storage.Close(); err != nil {
			return err
		}

		return b.stopProfiling()
	}); err != nil {
		return err
	}

	return nil
}

func (b *writeBenchmark) ingestScrapes(lbls []labels.Labels, scrapeCount int) (uint64, error) {
	var total atomic.Uint64

	for i := 0; i < scrapeCount; i += 100 {
		var wg sync.WaitGroup
		lbls := lbls
		for len(lbls) > 0 {
			l := 1000
			if len(lbls) < 1000 {
				l = len(lbls)
			}
			batch := lbls[:l]
			lbls = lbls[l:]

			wg.Add(1)
			go func() {
				defer wg.Done()

				n, err := b.ingestScrapesShard(batch, 100, int64(timeDelta*i))
				if err != nil {
					// exitWithError(err)
					fmt.Println(" err", err)
				}
				total.Add(n)
			}()
		}
		wg.Wait()
	}
	fmt.Println("ingestion completed")

	return total.Load(), nil
}

func (b *writeBenchmark) ingestScrapesShard(lbls []labels.Labels, scrapeCount int, baset int64) (uint64, error) {
	ts := baset

	type sample struct {
		labels labels.Labels
		value  int64
		ref    *storage.SeriesRef
	}

	scrape := make([]*sample, 0, len(lbls))

	for _, m := range lbls {
		scrape = append(scrape, &sample{
			labels: m,
			value:  123456789,
		})
	}
	total := uint64(0)

	for i := 0; i < scrapeCount; i++ {
		app := b.storage.Appender(context.TODO())
		ts += timeDelta

		for _, s := range scrape {
			s.value += 1000

			var ref storage.SeriesRef
			if s.ref != nil {
				ref = *s.ref
			}

			ref, err := app.Append(ref, s.labels, ts, float64(s.value))
			if err != nil {
				panic(err)
			}

			if s.ref == nil {
				s.ref = &ref
			}
			total++
		}
		if err := app.Commit(); err != nil {
			return total, err
		}
	}
	return total, nil
}

func (b *writeBenchmark) startProfiling() error {
	var err error

	// Start CPU profiling.
	b.cpuprof, err = os.Create(filepath.Join(b.outPath, "cpu.prof"))
	if err != nil {
		return fmt.Errorf("bench: could not create cpu profile: %w", err)
	}
	if err := pprof.StartCPUProfile(b.cpuprof); err != nil {
		return fmt.Errorf("bench: could not start CPU profile: %w", err)
	}

	// Start memory profiling.
	b.memprof, err = os.Create(filepath.Join(b.outPath, "mem.prof"))
	if err != nil {
		return fmt.Errorf("bench: could not create memory profile: %w", err)
	}
	runtime.MemProfileRate = 64 * 1024

	// Start fatal profiling.
	b.blockprof, err = os.Create(filepath.Join(b.outPath, "block.prof"))
	if err != nil {
		return fmt.Errorf("bench: could not create block profile: %w", err)
	}
	runtime.SetBlockProfileRate(20)

	b.mtxprof, err = os.Create(filepath.Join(b.outPath, "mutex.prof"))
	if err != nil {
		return fmt.Errorf("bench: could not create mutex profile: %w", err)
	}
	runtime.SetMutexProfileFraction(20)
	return nil
}

func (b *writeBenchmark) stopProfiling() error {
	if b.cpuprof != nil {
		pprof.StopCPUProfile()
		b.cpuprof.Close()
		b.cpuprof = nil
	}
	if b.memprof != nil {
		if err := pprof.Lookup("heap").WriteTo(b.memprof, 0); err != nil {
			return fmt.Errorf("error writing mem profile: %w", err)
		}
		b.memprof.Close()
		b.memprof = nil
	}
	if b.blockprof != nil {
		if err := pprof.Lookup("block").WriteTo(b.blockprof, 0); err != nil {
			return fmt.Errorf("error writing block profile: %w", err)
		}
		b.blockprof.Close()
		b.blockprof = nil
		runtime.SetBlockProfileRate(0)
	}
	if b.mtxprof != nil {
		if err := pprof.Lookup("mutex").WriteTo(b.mtxprof, 0); err != nil {
			return fmt.Errorf("error writing mutex profile: %w", err)
		}
		b.mtxprof.Close()
		b.mtxprof = nil
		runtime.SetMutexProfileFraction(0)
	}
	return nil
}

func measureTime(stage string, f func() error) (time.Duration, error) {
	fmt.Printf(">> start stage=%s\n", stage)
	start := time.Now()
	if err := f(); err != nil {
		return 0, err
	}

	fmt.Printf(">> completed stage=%s duration=%s\n", stage, time.Since(start))
	return time.Since(start), nil
}

func readPrometheusLabels(r io.Reader, n int) ([]labels.Labels, error) {
	scanner := bufio.NewScanner(r)

	var mets []labels.Labels
	hashes := map[uint64]struct{}{}
	i := 0

	for scanner.Scan() && i < n {
		m := make([]labels.Label, 0, 10)

		r := strings.NewReplacer("\"", "", "{", "", "}", "")
		s := r.Replace(scanner.Text())

		labelChunks := strings.Split(s, ",")
		for _, labelChunk := range labelChunks {
			split := strings.Split(labelChunk, ":")
			m = append(m, labels.Label{Name: split[0], Value: split[1]})
		}
		ml := labels.New(m...) // This sorts by name - order of the k/v labels matters, don't assume we'll always receive them already sorted.
		h := ml.Hash()
		if _, ok := hashes[h]; ok {
			continue
		}
		mets = append(mets, ml)
		hashes[h] = struct{}{}
		i++
	}
	return mets, nil
}

func listBlocks(path string, humanReadable bool) error {
	db, err := tsdb.OpenDBReadOnly(path, "", nil)
	if err != nil {
		return err
	}
	defer func() {
		err = tsdb_errors.NewMulti(err, db.Close()).Err()
	}()
	blocks, err := db.Blocks()
	if err != nil {
		return err
	}
	printBlocks(blocks, true, humanReadable)
	return nil
}

func printBlocks(blocks []tsdb.BlockReader, writeHeader, humanReadable bool) {
	tw := tabwriter.NewWriter(os.Stdout, 13, 0, 2, ' ', 0)
	defer tw.Flush()

	if writeHeader {
		fmt.Fprintln(tw, "BLOCK ULID\tMIN TIME\tMAX TIME\tDURATION\tNUM SAMPLES\tNUM CHUNKS\tNUM SERIES\tSIZE")
	}

	for _, b := range blocks {
		meta := b.Meta()

		fmt.Fprintf(tw,
			"%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
			meta.ULID,
			getFormattedTime(meta.MinTime, humanReadable),
			getFormattedTime(meta.MaxTime, humanReadable),
			time.Duration(meta.MaxTime-meta.MinTime)*time.Millisecond,
			meta.Stats.NumSamples,
			meta.Stats.NumChunks,
			meta.Stats.NumSeries,
			getFormattedBytes(b.Size(), humanReadable),
		)
	}
}

func getFormattedTime(timestamp int64, humanReadable bool) string {
	if humanReadable {
		return time.Unix(timestamp/1000, 0).UTC().String()
	}
	return strconv.FormatInt(timestamp, 10)
}

func getFormattedBytes(bytes int64, humanReadable bool) string {
	if humanReadable {
		return units.Base2Bytes(bytes).String()
	}
	return strconv.FormatInt(bytes, 10)
}

func openBlock(path, blockID string) (*tsdb.DBReadOnly, tsdb.BlockReader, error) {
	db, err := tsdb.OpenDBReadOnly(path, "", nil)
	if err != nil {
		return nil, nil, err
	}

	if blockID == "" {
		blockID, err = db.LastBlockID()
		if err != nil {
			return nil, nil, err
		}
	}

	b, err := db.Block(blockID, tsdb.DefaultPostingsDecoderFactory)
	if err != nil {
		return nil, nil, err
	}

	return db, b, nil
}

func analyzeBlock(ctx context.Context, path, blockID string, limit int, runExtended bool, matchers string) error {
	var (
		selectors []*labels.Matcher
		err       error
	)
	if len(matchers) > 0 {
		selectors, err = parser.ParseMetricSelector(matchers)
		if err != nil {
			return err
		}
	}
	db, block, err := openBlock(path, blockID)
	if err != nil {
		return err
	}
	defer func() {
		err = tsdb_errors.NewMulti(err, db.Close()).Err()
	}()

	meta := block.Meta()
	fmt.Printf("Block ID: %s\n", meta.ULID)
	// Presume 1ms resolution that Prometheus uses.
	fmt.Printf("Duration: %s\n", (time.Duration(meta.MaxTime-meta.MinTime) * 1e6).String())
	fmt.Printf("Total Series: %d\n", meta.Stats.NumSeries)
	if len(matchers) > 0 {
		fmt.Printf("Matcher: %s\n", matchers)
	}
	ir, err := block.Index()
	if err != nil {
		return err
	}
	defer ir.Close()

	allLabelNames, err := ir.LabelNames(ctx, selectors...)
	if err != nil {
		return err
	}
	fmt.Printf("Label names: %d\n", len(allLabelNames))

	type postingInfo struct {
		key    string
		metric uint64
	}
	postingInfos := []postingInfo{}

	printInfo := func(postingInfos []postingInfo) {
		slices.SortFunc(postingInfos, func(a, b postingInfo) int {
			switch {
			case b.metric < a.metric:
				return -1
			case b.metric > a.metric:
				return 1
			default:
				return 0
			}
		})

		for i, pc := range postingInfos {
			if i >= limit {
				break
			}
			fmt.Printf("%d %s\n", pc.metric, pc.key)
		}
	}

	labelsUncovered := map[string]uint64{}
	labelpairsUncovered := map[string]uint64{}
	labelpairsCount := map[string]uint64{}
	entries := 0
	var (
		p    index.Postings
		refs []storage.SeriesRef
	)
	if len(matchers) > 0 {
		p, err = tsdb.PostingsForMatchers(ctx, ir, selectors...)
		if err != nil {
			return err
		}
		// Expand refs first and cache in memory.
		// So later we don't have to expand again.
		refs, err = index.ExpandPostings(p)
		if err != nil {
			return err
		}
		fmt.Printf("Matched series: %d\n", len(refs))
		p = index.NewListPostings(refs)
	} else {
		p, err = ir.Postings(ctx, "", "") // The special all key.
		if err != nil {
			return err
		}
	}

	chks := []chunks.Meta{}
	builder := labels.ScratchBuilder{}
	for p.Next() {
		if err = ir.Series(p.At(), &builder, &chks); err != nil {
			return err
		}
		// Amount of the block time range not covered by this series.
		uncovered := uint64(meta.MaxTime-meta.MinTime) - uint64(chks[len(chks)-1].MaxTime-chks[0].MinTime)
		builder.Labels().Range(func(lbl labels.Label) {
			key := lbl.Name + "=" + lbl.Value
			labelsUncovered[lbl.Name] += uncovered
			labelpairsUncovered[key] += uncovered
			labelpairsCount[key]++
			entries++
		})
	}
	if p.Err() != nil {
		return p.Err()
	}
	fmt.Printf("Postings (unique label pairs): %d\n", len(labelpairsUncovered))
	fmt.Printf("Postings entries (total label pairs): %d\n", entries)

	postingInfos = postingInfos[:0]
	for k, m := range labelpairsUncovered {
		postingInfos = append(postingInfos, postingInfo{k, uint64(float64(m) / float64(meta.MaxTime-meta.MinTime))})
	}

	fmt.Printf("\nLabel pairs most involved in churning:\n")
	printInfo(postingInfos)

	postingInfos = postingInfos[:0]
	for k, m := range labelsUncovered {
		postingInfos = append(postingInfos, postingInfo{k, uint64(float64(m) / float64(meta.MaxTime-meta.MinTime))})
	}

	fmt.Printf("\nLabel names most involved in churning:\n")
	printInfo(postingInfos)

	postingInfos = postingInfos[:0]
	for k, m := range labelpairsCount {
		postingInfos = append(postingInfos, postingInfo{k, m})
	}

	fmt.Printf("\nMost common label pairs:\n")
	printInfo(postingInfos)

	postingInfos = postingInfos[:0]
	for _, n := range allLabelNames {
		values, err := ir.SortedLabelValues(ctx, n, selectors...)
		if err != nil {
			return err
		}
		var cumulativeLength uint64
		for _, str := range values {
			cumulativeLength += uint64(len(str))
		}
		postingInfos = append(postingInfos, postingInfo{n, cumulativeLength})
	}

	fmt.Printf("\nLabel names with highest cumulative label value length:\n")
	printInfo(postingInfos)

	postingInfos = postingInfos[:0]
	for _, n := range allLabelNames {
		lv, err := ir.SortedLabelValues(ctx, n, selectors...)
		if err != nil {
			return err
		}
		postingInfos = append(postingInfos, postingInfo{n, uint64(len(lv))})
	}
	fmt.Printf("\nHighest cardinality labels:\n")
	printInfo(postingInfos)

	postingInfos = postingInfos[:0]
	lv, err := ir.SortedLabelValues(ctx, "__name__", selectors...)
	if err != nil {
		return err
	}
	for _, n := range lv {
		postings, err := ir.Postings(ctx, "__name__", n)
		if err != nil {
			return err
		}
		// Only intersect postings if matchers are specified.
		if len(matchers) > 0 {
			postings = index.Intersect(postings, index.NewListPostings(refs))
		}
		count := 0
		for postings.Next() {
			count++
		}
		if postings.Err() != nil {
			return postings.Err()
		}
		postingInfos = append(postingInfos, postingInfo{n, uint64(count)})
	}
	fmt.Printf("\nHighest cardinality metric names:\n")
	printInfo(postingInfos)

	if runExtended {
		return analyzeCompaction(ctx, block, ir, selectors)
	}

	return nil
}

func analyzeCompaction(ctx context.Context, block tsdb.BlockReader, indexr tsdb.IndexReader, matchers []*labels.Matcher) (err error) {
	var postingsr index.Postings
	if len(matchers) > 0 {
		postingsr, err = tsdb.PostingsForMatchers(ctx, indexr, matchers...)
	} else {
		n, v := index.AllPostingsKey()
		postingsr, err = indexr.Postings(ctx, n, v)
	}
	if err != nil {
		return err
	}

	chunkr, err := block.Chunks()
	if err != nil {
		return err
	}
	defer func() {
		err = tsdb_errors.NewMulti(err, chunkr.Close()).Err()
	}()

	totalChunks := 0
	floatChunkSamplesCount := make([]int, 0)
	floatChunkSize := make([]int, 0)
	histogramChunkSamplesCount := make([]int, 0)
	histogramChunkSize := make([]int, 0)
	histogramChunkBucketsCount := make([]int, 0)
	var builder labels.ScratchBuilder
	for postingsr.Next() {
		var chks []chunks.Meta
		if err := indexr.Series(postingsr.At(), &builder, &chks); err != nil {
			return err
		}

		for _, chk := range chks {
			// Load the actual data of the chunk.
			chk, iterable, err := chunkr.ChunkOrIterable(chk)
			if err != nil {
				return err
			}
			// Chunks within blocks should not need to be re-written, so an
			// iterable is not expected to be returned from the chunk reader.
			if iterable != nil {
				return errors.New("ChunkOrIterable should not return an iterable when reading a block")
			}
			switch chk.Encoding() {
			case chunkenc.EncXOR:
				floatChunkSamplesCount = append(floatChunkSamplesCount, chk.NumSamples())
				floatChunkSize = append(floatChunkSize, len(chk.Bytes()))
			case chunkenc.EncFloatHistogram:
				histogramChunkSamplesCount = append(histogramChunkSamplesCount, chk.NumSamples())
				histogramChunkSize = append(histogramChunkSize, len(chk.Bytes()))
				fhchk, ok := chk.(*chunkenc.FloatHistogramChunk)
				if !ok {
					return errors.New("chunk is not FloatHistogramChunk")
				}
				it := fhchk.Iterator(nil)
				bucketCount := 0
				for it.Next() == chunkenc.ValFloatHistogram {
					_, f := it.AtFloatHistogram(nil)
					bucketCount += len(f.PositiveBuckets)
					bucketCount += len(f.NegativeBuckets)
				}
				histogramChunkBucketsCount = append(histogramChunkBucketsCount, bucketCount)
			case chunkenc.EncHistogram:
				histogramChunkSamplesCount = append(histogramChunkSamplesCount, chk.NumSamples())
				histogramChunkSize = append(histogramChunkSize, len(chk.Bytes()))
				hchk, ok := chk.(*chunkenc.HistogramChunk)
				if !ok {
					return errors.New("chunk is not HistogramChunk")
				}
				it := hchk.Iterator(nil)
				bucketCount := 0
				for it.Next() == chunkenc.ValHistogram {
					_, f := it.AtHistogram(nil)
					bucketCount += len(f.PositiveBuckets)
					bucketCount += len(f.NegativeBuckets)
				}
				histogramChunkBucketsCount = append(histogramChunkBucketsCount, bucketCount)
			}
			totalChunks++
		}
	}

	fmt.Printf("\nCompaction analysis:\n")
	fmt.Println()
	displayHistogram("samples per float chunk", floatChunkSamplesCount, totalChunks)

	displayHistogram("bytes per float chunk", floatChunkSize, totalChunks)

	displayHistogram("samples per histogram chunk", histogramChunkSamplesCount, totalChunks)

	displayHistogram("bytes per histogram chunk", histogramChunkSize, totalChunks)

	displayHistogram("buckets per histogram chunk", histogramChunkBucketsCount, totalChunks)
	return nil
}

type SeriesSetFormatter func(series storage.SeriesSet) error

func dumpSamples(ctx context.Context, dbDir, sandboxDirRoot string, mint, maxt int64, match []string, formatter SeriesSetFormatter) (err error) {
	db, err := tsdb.OpenDBReadOnly(dbDir, sandboxDirRoot, nil)
	if err != nil {
		return err
	}
	defer func() {
		err = tsdb_errors.NewMulti(err, db.Close()).Err()
	}()
	q, err := db.Querier(mint, maxt)
	if err != nil {
		return err
	}
	defer q.Close()

	matcherSets, err := parser.ParseMetricSelectors(match)
	if err != nil {
		return err
	}

	var ss storage.SeriesSet
	if len(matcherSets) > 1 {
		var sets []storage.SeriesSet
		for _, mset := range matcherSets {
			sets = append(sets, q.Select(ctx, true, nil, mset...))
		}
		ss = storage.NewMergeSeriesSet(sets, 0, storage.ChainedSeriesMerge)
	} else {
		ss = q.Select(ctx, false, nil, matcherSets[0]...)
	}

	err = formatter(ss)
	if err != nil {
		return err
	}

	if ws := ss.Warnings(); len(ws) > 0 {
		return tsdb_errors.NewMulti(ws.AsErrors()...).Err()
	}

	if ss.Err() != nil {
		return ss.Err()
	}
	return nil
}

func formatSeriesSet(ss storage.SeriesSet) error {
	for ss.Next() {
		series := ss.At()
		lbs := series.Labels()
		it := series.Iterator(nil)
		for it.Next() == chunkenc.ValFloat {
			ts, val := it.At()
			fmt.Printf("%s %g %d\n", lbs, val, ts)
		}
		for it.Next() == chunkenc.ValFloatHistogram {
			ts, fh := it.AtFloatHistogram(nil)
			fmt.Printf("%s %s %d\n", lbs, fh.String(), ts)
		}
		for it.Next() == chunkenc.ValHistogram {
			ts, h := it.AtHistogram(nil)
			fmt.Printf("%s %s %d\n", lbs, h.String(), ts)
		}
		if it.Err() != nil {
			return ss.Err()
		}
	}
	return nil
}

// CondensedString is labels.Labels.String() without spaces after the commas.
func CondensedString(ls labels.Labels) string {
	var b bytes.Buffer

	b.WriteByte('{')
	i := 0
	ls.Range(func(l labels.Label) {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(l.Name)
		b.WriteByte('=')
		b.WriteString(strconv.Quote(l.Value))
		i++
	})
	b.WriteByte('}')
	return b.String()
}

func formatSeriesSetOpenMetrics(ss storage.SeriesSet) error {
	for ss.Next() {
		series := ss.At()
		lbs := series.Labels()
		metricName := lbs.Get(labels.MetricName)
		lbs = lbs.DropMetricName()
		it := series.Iterator(nil)
		for it.Next() == chunkenc.ValFloat {
			ts, val := it.At()
			fmt.Printf("%s%s %g %.3f\n", metricName, CondensedString(lbs), val, float64(ts)/1000)
		}
		if it.Err() != nil {
			return ss.Err()
		}
	}
	fmt.Println("# EOF")
	return nil
}

func checkErr(err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func backfillOpenMetrics(path, outputDir string, humanReadable, quiet bool, maxBlockDuration time.Duration, customLabels map[string]string) int {
	inputFile, err := fileutil.OpenMmapFile(path)
	if err != nil {
		return checkErr(err)
	}
	defer inputFile.Close()

	if err := os.MkdirAll(outputDir, 0o777); err != nil {
		return checkErr(fmt.Errorf("create output dir: %w", err))
	}

	return checkErr(backfill(5000, inputFile.Bytes(), outputDir, humanReadable, quiet, maxBlockDuration, customLabels))
}

func displayHistogram(dataType string, datas []int, total int) {
	if len(datas) == 0 {
		fmt.Printf("%s: N/A\n\n", dataType)
		return
	}
	slices.Sort(datas)
	start, end, step := generateBucket(datas[0], datas[len(datas)-1])
	sum := 0
	buckets := make([]int, (end-start)/step+1)
	maxCount := 0
	for _, c := range datas {
		sum += c
		buckets[(c-start)/step]++
		if buckets[(c-start)/step] > maxCount {
			maxCount = buckets[(c-start)/step]
		}
	}
	avg := sum / len(datas)
	fmt.Printf("%s (min/avg/max): %d/%d/%d\n", dataType, datas[0], avg, datas[len(datas)-1])
	maxLeftLen := strconv.Itoa(len(strconv.Itoa(end)))
	maxRightLen := strconv.Itoa(len(strconv.Itoa(end + step)))
	maxCountLen := strconv.Itoa(len(strconv.Itoa(maxCount)))
	for bucket, count := range buckets {
		percentage := 100.0 * count / total
		fmt.Printf("[%"+maxLeftLen+"d, %"+maxRightLen+"d]: %"+maxCountLen+"d %s\n", bucket*step+start+1, (bucket+1)*step+start, count, strings.Repeat("#", percentage))
	}
	fmt.Println()
}

func generateBucket(minVal, maxVal int) (start, end, step int) {
	s := (maxVal - minVal) / 10

	step = 10
	for step < s && step <= 10000 {
		step *= 10
	}

	start = minVal - minVal%step
	end = maxVal - maxVal%step + step

	return
}
