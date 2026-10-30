package corrections

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestParseSkipsCommentsAndBrokenLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrections.txt")
	body := "# a comment\n\n" +
		"111\tretry\n" +
		"222  skip  spotify:track:abc\n" +
		"this line is broken\n" +
		"333\tskip\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(path)
	if err == nil {
		t.Fatal("a broken line must be reported")
	}
	want := []Correction{{ID: "111", Action: Retry}, {ID: "222", Action: Skip, URI: "spotify:track:abc"}}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if _, err := Parse(filepath.Join(t.TempDir(), "missing.txt")); err != nil {
		t.Errorf("a missing file is no correction, got %v", err)
	}
}

func TestRecordKeepsTheRestOfTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrections.txt")
	if err := os.WriteFile(path, []byte("# keep me\n111\tretry\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Record(path, Correction{ID: "111", Action: Skip, URI: "spotify:track:abc"}); err != nil {
		t.Fatal(err)
	}
	if err := Record(path, Correction{ID: "222", Action: Retry}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# keep me\n111\tskip\tspotify:track:abc\n222\tretry\n"
	if string(data) != want {
		t.Errorf("file =\n%s\nwant\n%s", data, want)
	}
}
