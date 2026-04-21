package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/prometheus/common/promslog"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/stretchr/testify/require"
)

type readVariant string

const timeDelta = time.Second

func buildDB(t testing.TB, dbDir string, variant string, eb float64) *tsdb.DatasetLoader {
	// info, err := os.Stat(dbDir)
	// if err != nil || !info.IsDir() {
	require.NoError(t, os.RemoveAll(dbDir))
	require.NoError(t, os.MkdirAll(dbDir, 0o777))
	// }

	dl := tsdb.NewDatasetLoader()

	logger := promslog.New(&promslog.Config{})

	switch variant {
	case "topcdb":
		chunkenc.DefaultCtype = chunkenc.Huffman
	case "topcdb_plus":
		chunkenc.DefaultCtype = chunkenc.Auto
	default:
	}

	chunkenc.DefaultCtype = chunkenc.Huffman
	chunkenc.DefaultErrbound = eb
	st, err := tsdb.Open(dbDir, logger, nil, &tsdb.Options{
		RetentionDuration:    int64(3650 * 24 * time.Hour / time.Millisecond),
		MinBlockDuration:     int64(2 * time.Hour / time.Millisecond),
		MaxBlockDuration:     int64(162 * time.Hour / time.Millisecond),
		OutOfOrderTimeWindow: int64(2 * time.Hour / time.Millisecond),
		SamplesPerChunk:      1000,
		OutOfOrderCapMax:     255,
		WALSegmentSize:       0,
		ErrorBound:           eb,
	}, tsdb.NewDBStats())
	if err != nil {
		return nil
	}
	// st.DisableCompactions()

	require.NoError(t, dl.ReadDataset("ett", "/home/jzj/Datasets/ETT"))

	// tb.generateOOO()

	valid_lines := len(dl.Samples[0])
	for i := 0; i < len(dl.Samples); i += 1 {
		valid_lines = max(valid_lines, len(dl.Samples[i]))
	}

	ctx := context.Background()
	for line := 0; line < valid_lines; line += 1 {
		app := st.Appender(ctx)
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
		require.NoError(t, app.Commit())
	}

	require.NoError(t, st.Compact(ctx))
	require.NoError(t, st.Close())
	return dl
}

type queryResult struct {
	Metric map[string]string `json:"metric"`
	Values [][2]interface{}  `json:"values"` // values 是一个 [timestamp, value] 对的数组
}

func queryRangeOnce(t testing.TB, baseURL, expr string, start, end, step int64) (time.Duration, []queryResult) {
	u := fmt.Sprintf(
		"%s/api/v1/query_range?query=%s&start=%d&end=%d&step=%d",
		baseURL, url.QueryEscape(expr), start, end, step,
	)

	t0 := time.Now()
	resp, err := http.Get(u)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Received non-200 status code (%d): %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string        `json:"resultType"`
			Result     []queryResult `json:"result"` // 使用我们定义的新结构
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.Equal(t, "success", parsed.Status)

	// 返回解析后的完整结果
	return time.Since(t0), parsed.Data.Result
}

func measureQueryRange(t testing.TB, baseURL, expr string, start, end, step int64, warmup, rounds int) (latencyStats, []queryResult) {
	ds := make([]time.Duration, 0, rounds)
	var lastResult []queryResult // 保存最后一次查询的结果

	for i := 0; i < warmup; i++ {
		_, _ = queryRangeOnce(t, baseURL, expr, start, end, step)
	}
	for i := 0; i < rounds; i++ {
		d, res := queryRangeOnce(t, baseURL, expr, start, end, step)
		ds = append(ds, d)
		lastResult = res // 每次都更新结果
	}

	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })

	var sum time.Duration
	for _, d := range ds {
		sum += d
	}

	return latencyStats{
		Mean: sum / time.Duration(len(ds)),
		P50:  ds[len(ds)/2],
		P95:  ds[int(float64(len(ds)-1)*0.95)],
	}, lastResult // 返回最后一次的结果
}

func TestDatasetReadQueries(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in short mode")
	}

	errorBounds := []float64{1e-2}
	variants := []string{"topcdb"}

	for _, eb := range errorBounds {
		for _, v := range variants {
			name := fmt.Sprintf("%s_eb_%g", v, eb)
			t.Run(name, func(t *testing.T) {
				dir := filepath.Join("/home/jzj/topcdb/benchmark/Read"+v, "storage")
				buildDB(t, dir, v, eb)

				cfg := writeMinimalConfig(t, dir)
				port := freePort(t)
				baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

				prom := exec.Command(
					promPath,
					"-test.main",
					"--config.file="+cfg,
					"--storage.tsdb.path="+dir,
					fmt.Sprintf("--web.listen-address=127.0.0.1:%d", port),
				)

				stderr, err := prom.StderrPipe()
				require.NoError(t, err)
				require.NoError(t, prom.Start())
				go io.Copy(os.Stderr, stderr)

				defer func() {
					_ = prom.Process.Kill()
					_ = prom.Wait()
				}()

				waitReady(t, baseURL)

				latest := int64(69680)

				scenarios := []struct {
					name       string
					expr       string
					start, end int64
					step       int64
				}{
					{
						name:  "single_5m",
						expr:  `ett{FileID="2",CaseID="14"}`,
						start: latest - 300,
						end:   latest,
						step:  1,
					},
					{
						name:  "single_1h",
						expr:  `ett{FileID="2",CaseID="14"}`,
						start: latest - 3600,
						end:   latest,
						step:  1,
					},
					{
						name:  "agg_1h",
						expr:  `avg_over_time(ett{FileID="2",CaseID="14"}[5m])`,
						start: latest - 3600,
						end:   latest,
						step:  15,
					},
					{
						name:  "agg_history_2h",
						expr:  `avg_over_time(ett{FileID="2",CaseID="14"}[5m])`,
						start: 0,
						end:   2 * 3600,
						step:  300,
					},
				}

				for _, sc := range scenarios {
					st, result := measureQueryRange(t, baseURL, sc.expr, sc.start, sc.end, sc.step, 10, 50)

					// 打印统计信息和结果数量
					t.Logf(
						"variant=%s eb=%g scenario=%s p50_ms=%.3f p95_ms=%.3f mean_ms=%.3f result_series=%d",
						v, eb, sc.name, ms(st.P50), ms(st.P95), ms(st.Mean), len(result),
					)

					// 打印详细结果
					if len(result) > 0 {
						// 为了避免日志过长，可以只打印第一个序列的部分数据点
						firstSeries := result[0]
						t.Logf("First series metric: %v", firstSeries.Metric)

						// 打印前5个数据点
						pointsToShow := 5
						if len(firstSeries.Values) < 5 {
							pointsToShow = len(firstSeries.Values)
						}

						for i := 0; i < pointsToShow; i++ {
							t.Logf("  - Point %d: Timestamp=%v, Value=%v", i, firstSeries.Values[i][0], firstSeries.Values[i][1])
						}
					}
				}
			})
		}
	}
}
