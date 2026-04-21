package tsdb

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/stretchr/testify/require"
)

func buildReadBenchDB(tb testing.TB, opts *Options, step time.Duration, forceCompact bool) *DB {
	db := openTestDB(tb, opts, nil)

	ctx := context.Background()

	dl := NewDatasetLoader()
	if err := dl.ReadDataset("pamapv2", "/home/jzj/Datasets/PAMAP2_Dataset_mini"); err != nil {
		return nil
	}

	valid_lines := len(dl.Samples[0])
	for i := 0; i < len(dl.Samples); i += 1 {
		valid_lines = max(valid_lines, len(dl.Samples[i]))
	}

	for line := 0; line < valid_lines; line += 1 {
		app := db.Appender(ctx)
		for lbs := 0; lbs < len(dl.Samples); lbs += 1 {
			if line >= len(dl.Samples[lbs]) {
				continue
			}

			var ref storage.SeriesRef
			if dl.Scrape[lbs].Ref != nil {
				ref = *dl.Scrape[lbs].Ref
			}

			T := int64(line) * int64(step/time.Millisecond)
			ref, err := app.Append(ref, dl.Scrape[lbs].Labels, T, dl.Samples[lbs][line].V)
			require.NoError(tb, err)

			if dl.Scrape[lbs].Ref == nil {
				dl.Scrape[lbs].Ref = &ref
			}
		}
		require.NoError(tb, app.Commit())
	}

	if forceCompact {
		require.NoError(tb, db.Compact(context.Background()))
	}

	return db
}

func drainQuerier(tb testing.TB, db *DB, mint, maxt int64, ms ...*labels.Matcher) int {
	q, err := db.Querier(mint, maxt)
	require.NoError(tb, err)
	defer func() { require.NoError(tb, q.Close()) }()

	ss := q.Select(context.Background(), false, nil, ms...)
	total := 0

	for ss.Next() {
		it := ss.At().Iterator(nil)
		for vt := it.Next(); vt != chunkenc.ValNone; vt = it.Next() {
			switch vt {
			case chunkenc.ValFloat:
				_, _ = it.At()
				total++
			default:
				tb.Fatalf("unexpected sample type: %v", vt)
			}
		}
		require.NoError(tb, it.Err())
	}

	require.NoError(tb, ss.Err())
	require.Empty(tb, ss.Warnings())
	return total
}

func BenchmarkReadPath(b *testing.B) {
	variants := []struct {
		name  string
		apply func(*Options)
	}{
		{
			name: "TopcDBPlus",
			apply: func(o *Options) {
				o.RetentionDuration = int64(3650 * 24 * time.Hour / time.Millisecond)
				o.MinBlockDuration = int64(2 * time.Hour / time.Millisecond)
				o.MaxBlockDuration = int64(162 * time.Hour / time.Millisecond)
				o.SamplesPerChunk = 1000
				o.OutOfOrderCapMax = 255
				o.WALSegmentSize = 0
				o.ErrorBound = chunkenc.DefaultErrbound
			},
		},
	}

	for _, v := range variants {
		b.Run(v.name, func(b *testing.B) {
			// hot/head：控制在 1h 内，不触发 block compaction
			hotOpts := DefaultOptions()
			hotOpts.SamplesPerChunk = 1000
			v.apply(hotOpts)
			hotDB := buildReadBenchDB(b, hotOpts, 10*time.Second, false)
			defer func() { require.NoError(b, hotDB.Close()) }()

			// cold/block：24h 数据，写完后显式 compact
			coldOpts := DefaultOptions()
			coldOpts.SamplesPerChunk = 1000
			v.apply(coldOpts)
			coldDB := buildReadBenchDB(b, coldOpts, 10*time.Second, true)
			defer func() { require.NoError(b, coldDB.Close()) }()

			m1 := labels.MustNewMatcher(labels.MatchEqual, "FileID", "0")
			m2 := labels.MustNewMatcher(labels.MatchEqual, "CaseID", "0")

			hotEnd := int64(1*time.Hour/time.Millisecond) - int64(10*time.Second/time.Millisecond)
			coldEnd := int64(24*time.Hour/time.Millisecond) - int64(10*time.Second/time.Millisecond)

			cases := []struct {
				name       string
				db         *DB
				mint, maxt int64
				ms         []*labels.Matcher
			}{
				{
					name: "hot/single_5m",
					db:   hotDB,
					mint: hotEnd - int64(5*time.Minute/time.Millisecond),
					maxt: hotEnd,
					ms:   []*labels.Matcher{m1, m2},
				},
				{
					name: "cold/single_1h",
					db:   coldDB,
					mint: coldEnd - int64(1*time.Hour/time.Millisecond),
					maxt: coldEnd,
					ms:   []*labels.Matcher{m1, m2},
				},
				{
					name: "cold/fanout_1h",
					db:   coldDB,
					mint: coldEnd - int64(1*time.Hour/time.Millisecond),
					maxt: coldEnd,
					ms:   []*labels.Matcher{m1, m2},
				},
				{
					name: "cold/all_24h",
					db:   coldDB,
					mint: 0,
					maxt: coldEnd,
					ms:   []*labels.Matcher{m1},
				},
			}

			for _, tc := range cases {
				tc := tc
				b.Run(tc.name, func(b *testing.B) {
					b.ReportAllocs()

					var total int
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						total += drainQuerier(b, tc.db, tc.mint, tc.maxt, tc.ms...)
					}
					b.StopTimer()

					if total == 0 {
						b.Fatalf("no samples returned")
					} else {
						b.Logf("total samples: %d", total)
					}
				})
			}
		})
	}
}
