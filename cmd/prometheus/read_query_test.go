package main

import (
	"context"
	"encoding/json" // 1. 导入 "encoding/json" 包
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"testing"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	promtsdb "github.com/prometheus/prometheus/tsdb"
	"github.com/stretchr/testify/require"
)

type latencyStats struct {
	Mean time.Duration
	P50  time.Duration
	P95  time.Duration
}

func freePort(t testing.TB) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func writeMinimalConfig(t testing.TB, dir string) string {
	cfg := dir + "/prometheus.yml"
	content := []byte("global:\n  scrape_interval: 1m\nscrape_configs: []\n")
	require.NoError(t, os.WriteFile(cfg, content, 0o644))
	return cfg
}

func buildReadDBOnDisk(t testing.TB, dir string, apply func(*promtsdb.Options)) {
	opts := promtsdb.DefaultOptions()
	opts.SamplesPerChunk = 1000
	apply(opts)

	db, err := promtsdb.Open(dir, nil, nil, opts, nil)
	require.NoError(t, err)

	ctx := context.Background()
	app := db.Appender(ctx)

	step := 10 * time.Second
	duration := 24 * time.Hour
	samplesPerSeries := int(duration / step)
	stepMS := int64(step / time.Millisecond)

	for s := 0; s < 4; s++ {
		lset := labels.FromStrings(
			labels.MetricName, "bench_metric",
			"job", "bench",
			"instance", fmt.Sprintf("inst_%05d", s),
			"group", fmt.Sprintf("g_%02d", s%16),
		)
		for i := 0; i < samplesPerSeries; i++ {
			ts := int64(i) * stepMS
			v := float64((i % 1000) + (s % 17))
			_, err := app.Append(0, lset, ts, v)
			require.NoError(t, err)
		}
	}

	require.NoError(t, app.Commit())
	require.NoError(t, db.Compact(ctx))
	require.NoError(t, db.Close())
}

func waitReady(t testing.TB, baseURL string) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/-/ready")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("prometheus not ready: %s", baseURL)
}

// 2. 修改 MeasureQueryRange 的返回值，增加一个 int 用于返回系列数
func MeasureQueryRange(t testing.TB, baseURL, expr string, start, end, step int64, warmup, rounds int) (latencyStats, int) {
	var ds []time.Duration
	var resultCount int // 用于存储系列数

	// 3. 修改 runOnce 的返回值
	runOnce := func() (time.Duration, int) {
		u := fmt.Sprintf(
			"%s/api/v1/query_range?query=%s&start=%d&end=%d&step=%d",
			baseURL, url.QueryEscape(expr), start, end, step,
		)

		t0 := time.Now()
		resp, err := http.Get(u)
		require.NoError(t, err)
		// 4. 读取并解析响应体，而不是丢弃它
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// 5. 解析JSON以获取系列数
		var parsed struct {
			Data struct {
				Result []json.RawMessage `json:"result"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(body, &parsed))

		return time.Since(t0), len(parsed.Data.Result)
	}

	for i := 0; i < warmup; i++ {
		_, _ = runOnce()
	}
	for i := 0; i < rounds; i++ {
		// 6. 接收系列数
		d, n := runOnce()
		ds = append(ds, d)
		resultCount = n // 保存最后一次查询的系列数
	}

	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })

	var sum time.Duration
	for _, d := range ds {
		sum += d
	}

	p50 := ds[len(ds)/2]
	p95 := ds[int(float64(len(ds)-1)*0.95)]

	stats := latencyStats{
		Mean: sum / time.Duration(len(ds)),
		P50:  p50,
		P95:  p95,
	}
	// 7. 返回统计数据和系列数
	return stats, resultCount
}

func ms(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func TestAPIReadLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	variants := []struct {
		name  string
		apply func(*promtsdb.Options)
	}{
		{
			name: "Prometheus",
			apply: func(o *promtsdb.Options) {
				// TODO: 用你现在 baseline 的配置替换这里
				o.ErrorBound = 0
			},
		},
		{
			name: "TopcDB",
			apply: func(o *promtsdb.Options) {
				// TODO: 复用你现有 TopcDB 写实验的配置入口
				o.ErrorBound = 1e-3
			},
		},
		{
			name: "TopcDBPlus",
			apply: func(o *promtsdb.Options) {
				// TODO: 复用你现有 TopcDB+ 写实验的配置入口
				o.ErrorBound = 1e-3
			},
		},
	}

	for _, v := range variants {
		v := v
		t.Run(v.name, func(t *testing.T) {
			dir := t.TempDir()
			buildReadDBOnDisk(t, dir, v.apply)

			cfg := writeMinimalConfig(t, dir)
			port := freePort(t)
			baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

			prom := exec.Command(
				promPath,
				"-test.main",
				"--config.file="+cfg,
				fmt.Sprintf("--web.listen-address=127.0.0.1:%d", port),
				"--storage.tsdb.path="+dir,
			)

			stderr, err := prom.StderrPipe()
			require.NoError(t, err)
			require.NoError(t, prom.Start())
			go io.Copy(io.Discard, stderr)

			defer func() {
				_ = prom.Process.Kill()
				_ = prom.Wait()
			}()

			waitReady(t, baseURL)

			end := int64((24*time.Hour - 10*time.Second) / time.Second)

			scenarios := []struct {
				name       string
				expr       string
				start, end int64
				step       int64
			}{
				{
					name:  "single_1h",
					expr:  `bench_metric{instance="inst_00000"}`,
					start: end - 3600,
					end:   end,
					step:  15,
				},
				{
					name:  "fanout_1h",
					expr:  `bench_metric{group="g_00"}`,
					start: end - 3600,
					end:   end,
					step:  15,
				},
				{
					name:  "agg_24h",
					expr:  `sum by (group) (avg_over_time(bench_metric{job="bench"}[5m]))`,
					start: end - 24*3600,
					end:   end,
					step:  60,
				},
			}

			for _, sc := range scenarios {
				// 8. 接收返回的系列数 resultN
				st, resultN := MeasureQueryRange(t, baseURL, sc.expr, sc.start, sc.end, sc.step, 5, 30)
				// 9. 在日志中打印系列数
				t.Logf(
					"variant=%s,scenario=%s,p50_ms=%.3f,p95_ms=%.3f,mean_ms=%.3f,result_series=%d",
					v.name, sc.name, ms(st.P50), ms(st.P95), ms(st.Mean), resultN,
				)
			}
		})
	}
}
