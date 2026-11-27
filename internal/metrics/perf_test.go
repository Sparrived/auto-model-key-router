package metrics

import (
	"path/filepath"
	"testing"
)

func BenchmarkStoreRecordWithSideTables(b *testing.B) {
	store, err := Open(filepath.Join(b.TempDir(), "bench.sqlite3"))
	if err != nil {
		b.Fatalf("Open 失败: %v", err)
	}
	defer func() { _ = store.Close() }()

	status := int64(200)
	provider := "openai"
	upstream := "gpt-4o-mini"
	params := RecordParams{
		ModelID:         "gpt-4o-mini",
		KeyName:         "main",
		StatusCode:      &status,
		DurationMS:      120,
		FirstTokenMS:    30,
		CallerType:      "access_key",
		ProviderID:      &provider,
		UpstreamModelID: &upstream,
		Workspace:       "default",
		AccessKeyID:     "ak-1",
		ClientAddr:      "127.0.0.1:50000",
		UserAgent:       "bench",
		Stream:          true,
		APIFormat:       "chat/completions",
		ReasoningEffort: "medium",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.Record(params); err != nil {
			b.Fatalf("Record 失败: %v", err)
		}
	}
}
