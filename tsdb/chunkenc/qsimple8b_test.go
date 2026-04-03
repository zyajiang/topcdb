package chunkenc

import (
	"fmt"
	"math"
	"testing"
)

func generateFloat64Slice(n int) []float64 {
	src := make([]float64, n)
	for i := 0; i < n; i++ {
		src[i] = math.Sin(float64(1)/360) * 1e5
	}
	return src
}

func TestCorrectness(t *testing.T) {
	// ErrorBound is absolute error bound for compression algorithms.
	ErrorBound := 1e-6
	// The Whole dataset will be divided into chunks of ChunkSize for compression.
	ChunkSize := 1000
	CompressTypes := map[string]CompressType{
		"Simplebits": Simplebits,
		"QSimple8b":  QSimple8b,
		"BitPacking": BitPacking,
		"Huffman":    Huffman,
		// "SZ3":        SZ3,
		// "Machete":    Machete,
		// "MOST":       MOST,
		"Auto": Auto}
	samples := generateFloat64Slice(ChunkSize)
	for name, ctype := range CompressTypes {

		chk := NewCLChunk()
		app, _ := chk.Appender()
		app.(*CLAppender).SetCompressType(ctype)
		app.(*CLAppender).SetErrorBound(ErrorBound)

		for i := 0; i < ChunkSize; i++ {
			app.(*CLAppender).AppendQuantizer(int64(i), 30)
			// app.Append(int64(i), samples[i])
		}

		app.(*CLAppender).Compact()

		it := chk.Iterator(nil)
		for it.Next() != ValNone {
			t, v := it.At()
			if math.Abs(v-0.00006) > (1+0.05)*ErrorBound {
				fmt.Printf("%s Precision Error > (%d,%f) (%f)\n", name, t, v, samples[t])
				return
			}
		}

		fmt.Printf("%s compression ratio: %.2f\n", name, float64(ChunkSize*8)/float64(len(chk.Bytes())))
	}
}
