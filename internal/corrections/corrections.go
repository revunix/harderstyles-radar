// Package corrections reads and writes the file of corrections: the releases whose Spotify track the user
// refused. The file is the one place a correction is kept, so the interface and a run without it do the same.
package corrections

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// Retry drops the remembered track, so the next run looks the release up again.
	Retry = "retry"
	// Skip refuses one Spotify track for good; the next run looks for another.
	Skip = "skip"
	// Force takes one Spotify track even though the match would not, for instance because the date is off.
	Force = "force"
)

// Correction is one line of the file.
type Correction struct {
	ID     string // the track id on hardstyle.com
	Action string // Retry or Skip
	URI    string // the refused Spotify track, only for Skip
}

// Parse reads the corrections from the file. A missing file is no correction. A line that cannot be read is
// reported and skipped; the rest still counts.
func Parse(path string) ([]Correction, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Correction
	var bad []string
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			bad = append(bad, fmt.Sprintf("%d", n))
			continue
		}
		c := Correction{ID: fields[0], Action: fields[1]}
		switch c.Action {
		case Retry:
		case Skip, Force:
			if len(fields) < 3 {
				bad = append(bad, fmt.Sprintf("%d", n))
				continue
			}
			c.URI = fields[2]
		default:
			bad = append(bad, fmt.Sprintf("%d", n))
			continue
		}
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(bad) > 0 {
		return out, fmt.Errorf("line %s", strings.Join(bad, ", "))
	}
	return out, nil
}

// Record adds a correction, or replaces the line already there for the same release. Everything else in the
// file stays as it is, including comments. The file is created when it does not exist yet.
func Record(path string, c Correction) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	line := c.ID + "\t" + c.Action
	if c.URI != "" {
		line += "\t" + c.URI
	}
	var b strings.Builder
	replaced := false
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		text := sc.Text()
		fields := strings.Fields(strings.TrimSpace(text))
		if len(fields) >= 2 && fields[0] == c.ID && (fields[1] == Retry || fields[1] == Skip || fields[1] == Force) {
			text, replaced = line, true
		}
		b.WriteString(text)
		b.WriteByte('\n')
	}
	if !replaced {
		if b.Len() == 0 {
			b.WriteString("# Corrections for harderstyles-radar. One release per line:\n")
			b.WriteString("#   <id>  retry                 look the release up again\n")
			b.WriteString("#   <id>  skip  <spotify-uri>   never take this Spotify track\n")
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil && dir != "." {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
