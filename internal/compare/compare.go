// Package compare reads the release lists of every configured source (hardstyle.com, hardtunes.com) and
// compares them with each other: what is on both sites, what only on one.
package compare

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"hardstylesync/internal/config"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/hardtunes"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/match"
)

// Source is one release site with the lists that were read from it.
type Source struct {
	Name string // "hardstyle.com"
	// Genres maps the genre label to its releases, newest first.
	Genres map[string][]hardstyle.Track
	// keys maps the normalized key of every release to one of its tracks.
	keys map[string]hardstyle.Track
}

// Report is the comparison of all sources.
type Report struct {
	Sources []Source
	// Both holds one release per key that at least two sources have.
	Both []hardstyle.Track
}

// key makes two releases of different sites comparable: "Artist - Title", folded, without the mix suffixes
// ("Extended Mix" and friends) and articles.
func key(t hardstyle.Track) string {
	return match.NormName(t.Artist) + "|" + match.NormTitle(t.Title+" "+t.Version)
}

// Run reads cfg.Pages pages of every genre of every source and compares the lists.
func Run(ctx context.Context, hc *http.Client, cfg config.Config, logf func(string, ...any)) (Report, error) {
	sources := []struct {
		name   string
		genres []string
		fetch  func(ctx context.Context, client *http.Client, genre string, pages int) ([]hardstyle.Track, error)
	}{
		{"hardstyle.com", cfg.Genres, func(ctx context.Context, client *http.Client, genre string, pages int) ([]hardstyle.Track, error) {
			return hardstyle.FetchReleases(ctx, client, genre, pages)
		}},
		{"hardtunes.com", cfg.HardtunesGenres, func(ctx context.Context, client *http.Client, genre string, pages int) ([]hardstyle.Track, error) {
			tracks, err := hardtunes.FetchReleases(ctx, client, genre, pages)
			out := make([]hardstyle.Track, len(tracks))
			for i, tr := range tracks {
				out[i] = tr.Release()
			}
			return out, err
		}},
	}

	var rep Report
	for _, s := range sources {
		src := Source{Name: s.name, Genres: map[string][]hardstyle.Track{}, keys: map[string]hardstyle.Track{}}
		for _, genre := range s.genres {
			label := hardstyle.GenreLabel(genre)
			if s.name == "hardtunes.com" {
				label = hardtunes.GenreLabel(genre)
			}
			logf(i18n.T("%s %s: %d Seiten lesen", "%s %s: reading %d pages"), s.name, label, cfg.Pages)
			tracks, err := s.fetch(ctx, hc, genre, cfg.Pages)
			if err != nil {
				return rep, fmt.Errorf(i18n.T("%s %s lesen: %w", "reading %s %s: %w"), s.name, label, err)
			}
			logf(i18n.T("%s %s: %d Releases", "%s %s: %d releases"), s.name, label, len(tracks))
			src.Genres[label] = tracks
			for _, tr := range tracks {
				k := key(tr)
				if _, ok := src.keys[k]; !ok {
					src.keys[k] = tr
				}
			}
		}
		rep.Sources = append(rep.Sources, src)
	}

	// A key that at least two sources have is "on both"; the representative comes from the first source.
	count := map[string]int{}
	for _, src := range rep.Sources {
		for k := range src.keys {
			count[k]++
		}
	}
	seen := map[string]bool{}
	for _, src := range rep.Sources {
		for k, tr := range src.keys {
			if count[k] >= 2 && !seen[k] {
				seen[k] = true
				rep.Both = append(rep.Both, tr)
			}
		}
	}
	slices.SortFunc(rep.Both, func(a, b hardstyle.Track) int { return strings.Compare(key(a), key(b)) })
	return rep, nil
}

// only returns the releases of one source that no other source has.
func (r Report) only(src Source) []hardstyle.Track {
	var out []hardstyle.Track
	for k, tr := range src.keys {
		other := false
		for _, s2 := range r.Sources {
			if s2.Name != src.Name {
				if _, ok := s2.keys[k]; ok {
					other = true
					break
				}
			}
		}
		if !other {
			out = append(out, tr)
		}
	}
	slices.SortFunc(out, func(a, b hardstyle.Track) int { return strings.Compare(key(a), key(b)) })
	return out
}

// maxList caps the printed lists; the counts above them are always complete.
const maxList = 50

// Render writes the comparison as Markdown: a table per genre, then the summary and the lists of the
// releases that only one source has.
func Render(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", i18n.T("Vergleich der Quellen", "Source comparison"))

	// Every genre of every source, in the order of the sources.
	var genres []string
	for _, src := range r.Sources {
		for g := range src.Genres {
			if !slices.Contains(genres, g) {
				genres = append(genres, g)
			}
		}
	}
	slices.Sort(genres)

	fmt.Fprintf(&b, "| %s |", i18n.T("Genre", "Genre"))
	for _, src := range r.Sources {
		fmt.Fprintf(&b, " %s |", src.Name)
	}
	fmt.Fprintf(&b, " %s | %s | %s |\n|", i18n.T("gemeinsam", "both"), i18n.T("nur erste Quelle", "only first source"), i18n.T("nur zweite Quelle", "only second source"))
	for range r.Sources {
		b.WriteString("---|")
	}
	b.WriteString("---|---|---|\n")
	for _, g := range genres {
		fmt.Fprintf(&b, "| %s |", g)
		for _, src := range r.Sources {
			tracks := src.Genres[g]
			if tracks == nil {
				b.WriteString(" – |")
				continue
			}
			fmt.Fprintf(&b, " %d |", len(tracks))
		}
		if len(r.Sources) == 2 {
			a, c := r.Sources[0], r.Sources[1]
			ka, kc := keysOf(a, g), keysOf(c, g)
			both := intersect(ka, kc)
			fmt.Fprintf(&b, " %d | %d | %d |\n", len(both), len(ka)-len(both), len(kc)-len(both))
		} else {
			b.WriteString("  |  |  |\n")
		}
	}
	b.WriteString("\n")

	for _, src := range r.Sources {
		total := 0
		for _, tracks := range src.Genres {
			total += len(tracks)
		}
		only := r.only(src)
		fmt.Fprintf(&b, "- **%s**: %d %s, %d %s\n", src.Name, total, i18n.T("Releases gesamt", "releases in total"),
			len(only), i18n.T("nur auf dieser Seite", "only on this site"))
	}
	fmt.Fprintf(&b, "- **%s**: %d\n\n", i18n.T("auf beiden Seiten", "on both sites"), len(r.Both))

	for _, src := range r.Sources {
		only := r.only(src)
		fmt.Fprintf(&b, "### %s (%d)\n\n", fmt.Sprintf(i18n.T("Nur auf %s", "Only on %s"), src.Name), len(only))
		for i, tr := range only {
			if i >= maxList {
				fmt.Fprintf(&b, "- … %d %s\n", len(only)-maxList, i18n.T("weitere", "more"))
				break
			}
			fmt.Fprintf(&b, "- %s – %s%s (%s)\n", tr.Artist, tr.Title, versionSuffix(tr), tr.Genre)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func versionSuffix(t hardstyle.Track) string {
	if t.Version == "" {
		return ""
	}
	return " (" + t.Version + ")"
}

// keysOf returns the normalized keys of one genre of one source.
func keysOf(src Source, genre string) map[string]bool {
	out := map[string]bool{}
	for _, tr := range src.Genres[genre] {
		out[key(tr)] = true
	}
	return out
}

func intersect(a, b map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		if b[k] {
			out[k] = true
		}
	}
	return out
}
