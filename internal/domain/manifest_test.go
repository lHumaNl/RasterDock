package domain

import (
	"strings"
	"testing"
)

func TestManifestPaths(t *testing.T) {
	for _, p := range []string{"../escape", "/absolute", "a/../b", "a\\b", "CON", "a:b", "x/", "x\x00"} {
		if ValidPath(p) {
			t.Errorf("accepted %q", p)
		}
	}
	for _, p := range []string{"текст/данные.bin", "a/b.txt"} {
		if !ValidPath(p) {
			t.Errorf("rejected %q", p)
		}
	}
}

func TestManifestValidation(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000000"
	m := Manifest{TransferID: id, BlockSize: 64, Files: []File{{ID: 1, Path: "a", SourceHash: strings.Repeat("0", 64), EncodedHash: strings.Repeat("0", 64), Encoding: "raw"}}}
	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadManifest(strings.NewReader(string(b)))
	if err != nil || len(got.Files) != 1 {
		t.Fatal(got, err)
	}
	if _, err := ReadManifest(strings.NewReader(string(b) + "{}\n")); err == nil {
		t.Fatal("trailing record")
	}
	m.Files[0].Chunks = 1
	if _, err := m.Encode(); err == nil {
		t.Fatal("invalid empty chunks")
	}
}

func TestManifestCaseFoldedParentCollision(t *testing.T) {
	m := Manifest{TransferID: "00000000-0000-4000-8000-000000000000", BlockSize: 64}
	for i, p := range []string{"A/x", "a"} {
		m.Files = append(m.Files, File{ID: uint32(i + 1), Path: p, SourceHash: strings.Repeat("0", 64), EncodedHash: strings.Repeat("0", 64), Encoding: "raw"})
	}
	if err := m.Validate(); err == nil {
		t.Fatal("case-folded file/directory collision accepted")
	}
}
