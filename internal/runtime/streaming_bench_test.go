package runtime

import (
	"context"
	"io"
	"testing"
	"time"
)

type sliceStreamReader struct {
	chunks [][]byte
	index  int
}

func (r *sliceStreamReader) Next(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.index >= len(r.chunks) {
		return nil, io.EOF
	}
	chunk := r.chunks[r.index]
	r.index++
	return chunk, nil
}

func BenchmarkIterStreamBytes500Chunks(b *testing.B) {
	chunks := make([][]byte, 500)
	for i := range chunks {
		chunks[i] = []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reader := &sliceStreamReader{chunks: chunks}
		count := 0
		err := IterStreamBytes(
			context.Background(),
			reader,
			time.Now().Add(5*time.Second),
			5*time.Second,
			func(chunk []byte) error {
				count += len(chunk)
				return nil
			},
		)
		if err != nil {
			b.Fatalf("IterStreamBytes 失败: %v", err)
		}
	}
}
