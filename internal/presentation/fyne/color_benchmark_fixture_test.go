package fyneui

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
)

const colorBenchmarkBytes = 128 * 1024
const gridBenchmarkBytes = 512 * 1024

func colorBenchmarkFixture() []byte {
	return benchmarkPayload(colorBenchmarkBytes)
}

func gridBenchmarkFixture() []byte {
	return benchmarkPayload(gridBenchmarkBytes)
}

func benchmarkPayload(size int) []byte {
	b := make([]byte, size)
	_, _ = rand.New(rand.NewSource(20261026)).Read(b) //nolint:gosec // Reproducible non-secret benchmark fixture, not a security token.
	return b
}

func gridBenchmarkOrder() [3][3]int {
	return [3][3]int{{1, 4, 6}, {4, 6, 1}, {6, 1, 4}}
}

func gridBenchmarkProfile(cells int) (application.VisualProfile, error) {
	p := application.DefaultVisualProfile(application.Colors8)
	switch cells {
	case 1:
	case 4:
		p.Geometry.Columns, p.Geometry.Rows = 2, 2
	case 6:
		p.Geometry.Columns, p.Geometry.Rows = 3, 2
	default:
		return application.VisualProfile{}, errors.New("unsupported benchmark grid")
	}
	return p, nil
}

func TestColorBenchmarkFixtureStaysRaw(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "payload.bin")
	if err := os.WriteFile(source, colorBenchmarkFixture(), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := application.Prepare(context.Background(), source, filepath.Join(root, "prepared"), 512, storage.Flush, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Manifest.Files) != 1 || p.Manifest.Files[0].Encoding != "raw" || p.Manifest.Files[0].EncodedSize != colorBenchmarkBytes {
		t.Fatal("fixture must exercise 128 KiB raw, not compressed throughput")
	}
}

func colorBenchmarkOrder() [3][3]int {
	return [3][3]int{{2, 4, 8}, {4, 8, 2}, {8, 2, 4}}
}

func TestColorBenchmarkPlan(t *testing.T) {
	a, b := colorBenchmarkFixture(), colorBenchmarkFixture()
	if len(a) != 131072 || !bytes.Equal(a, b) {
		t.Fatal("fixture must be reproducible and exactly 128 KiB")
	}
	for position := 0; position < 3; position++ {
		seen := map[int]bool{}
		for _, row := range colorBenchmarkOrder() {
			seen[row[position]] = true
		}
		if !seen[2] || !seen[4] || !seen[8] {
			t.Fatal("each profile must occupy each order position once")
		}
	}
}
