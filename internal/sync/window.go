package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/match"
)

// errEndOfList is what a feed's fetch answers for a page past the last one.
var errEndOfList = errors.New("end of list")

// feed is one list of a site (one genre), read page by page only as far as the window needs it. The lists of
// both sites are sorted newest first, so the newest releases of all lists are found like a zipper: again and
// again the newest of the heads of the lists.
type feed struct {
	name     string                                                         // "hardstyle.com Hardstyle", for the log
	genre    string                                                         // "Hardstyle": the same genre on both sites has the same name
	fetch    func(ctx context.Context, page int) ([]hardstyle.Track, error) // errEndOfList past the last page
	delay    time.Duration                                                  // pause before every page but the first
	maxPages int
	// stopAt ends the list after the first page with a release it names: a check for new tracks only needs
	// what is newer than the releases it knows.
	stopAt func(hardstyle.Track) bool

	page      int // pages read
	buf       []hardstyle.Track
	done      bool
	last      string // release date of the release taken last from this list
	headDate  string
	headKnown bool
	taken     int   // releases of this list in the window
	err       error // the list could not be read (any more); the run goes on without the rest of it
	empty     bool  // already the first page had nothing: the genre is empty, or the site changed
}

// fill makes sure the feed has a head, reading its next page when the last one is used up. A page that cannot
// be read ends the list for this run (err); only a cancelled run is an error.
func (f *feed) fill(ctx context.Context) error {
	for len(f.buf) == 0 && !f.done {
		if f.page >= f.maxPages {
			f.done = true
			break
		}
		if f.page > 0 {
			select {
			case <-time.After(f.delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		f.page++
		tracks, err := f.fetch(ctx, f.page)
		switch {
		case errors.Is(err, errEndOfList):
			f.done, f.empty = true, f.page == 1
		case ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			f.done, f.err = true, err
		default:
			f.buf = tracks
			if f.stopAt != nil && slices.ContainsFunc(tracks, f.stopAt) {
				f.done = true // the releases of this page are still taken, no page after it is read
			}
		}
	}
	return nil
}

// farFuture ranks the first release of a list whose date is unknown before everything: the lists are newest
// first, so it is at least as new as the rest.
const farFuture = "9999-12-31"

// windowOptions say how many releases the window takes and how it learns about them.
type windowOptions struct {
	limit int
	// minPerGenre releases of every genre are taken first (the newest of that genre on both sites), so a
	// small genre is not pushed out by the busy ones; the rest of the window goes by date. 0 = by date only.
	minPerGenre int
	// dateOf gives the release date of a release (yyyy-mm-dd, "" when unknown).
	dateOf func(context.Context, hardstyle.Track) string
	// isrcOf gives the ISRC of a release when it is known already ("" else): the surest sign that two
	// entries are the same recording.
	isrcOf func(hardstyle.Track) string
	// tracksOf tells the Spotify tracks a release has, as far as it is known (known false: not looked up yet).
	// With it the window takes more than limit releases, until their tracks can fill limit: the releases Spotify
	// does not have would leave the playlist short. A release not looked up yet counts as one track. nil = limit
	// releases.
	tracksOf func(hardstyle.Track) (uris []string, known bool)
	// progress is told the number of releases taken so far; nil = no report.
	progress func(taken int)
}

// window is the merge in progress: the releases taken, their dates, and where each identity of a release
// stands, so a release on both sites (or in two genres) is taken once.
type merge struct {
	o     windowOptions
	out   []hardstyle.Track
	dates []string
	at    map[string]int
}

// take takes the newest releases of the given feeds until the window holds n releases or the feeds are used up.
func (m *merge) take(ctx context.Context, feeds []*feed, n int) error {
	for len(m.out) < n {
		best := -1
		for i, f := range feeds {
			if err := f.fill(ctx); err != nil {
				return err
			}
			if len(f.buf) == 0 {
				continue
			}
			if !f.headKnown {
				d := m.o.dateOf(ctx, f.buf[0])
				if err := ctx.Err(); err != nil {
					return err
				}
				switch {
				case d != "":
				case f.last != "":
					d = f.last // as new as the one before it in its list
				default:
					d = farFuture
				}
				f.headDate, f.headKnown = d, true
			}
			if best < 0 || f.headDate > feeds[best].headDate {
				best = i
			}
		}
		if best < 0 {
			return nil // every list is used up
		}
		f := feeds[best]
		t, d := f.buf[0], f.headDate
		f.buf, f.last, f.headKnown = f.buf[1:], d, false
		keys := []string{"release:" + releaseKey(t)}
		if m.o.isrcOf != nil {
			if isrc := m.o.isrcOf(t); isrc != "" {
				keys = append(keys, "isrc:"+isrc)
			}
		}
		if j, ok := m.find(keys); ok {
			if fromHardtunes(m.out[j]) && !fromHardtunes(t) {
				m.out[j] = t // the copy from hardstyle.com wins, its page has the catalog number
			}
			for _, k := range keys {
				m.at[k] = j
			}
			continue
		}
		for _, k := range keys {
			m.at[k] = len(m.out)
		}
		m.out, m.dates = append(m.out, t), append(m.dates, d)
		f.taken++
		if m.o.progress != nil {
			m.o.progress(len(m.out))
		}
	}
	return nil
}

func (m *merge) find(keys []string) (int, bool) {
	for _, k := range keys {
		if j, ok := m.at[k]; ok {
			return j, true
		}
	}
	return 0, false
}

// tracks counts the tracks the releases taken so far are expected to give, every Spotify track once.
func (m *merge) tracks() int {
	n, seen := 0, map[string]bool{}
	for _, t := range m.out {
		uris, known := m.o.tracksOf(t)
		if !known {
			n++
			continue
		}
		for _, u := range uris {
			if !seen[u] {
				seen[u] = true
				n++
			}
		}
	}
	return n
}

// newestReleases takes the newest o.limit releases of all feeds (more with o.tracksOf), newest first, reading no
// page more than that needs. A release without a date is as new as the one before it in its list. A release that
// is already in the window (same artists, title and version, or the same ISRC) is not taken twice; the copy from
// hardstyle.com wins. With o.minPerGenre every genre gets its newest releases first. For equal dates the earlier
// feed comes first. A list that cannot be read is left out (its feed keeps the error); only when no list can be
// read at all the window fails.
func newestReleases(ctx context.Context, feeds []*feed, o windowOptions) ([]hardstyle.Track, error) {
	m := &merge{o: o, at: map[string]int{}}
	if o.minPerGenre > 0 {
		var genres []string
		for _, f := range feeds {
			if !slices.Contains(genres, f.genre) {
				genres = append(genres, f.genre)
			}
		}
		for _, g := range genres {
			group := slices.DeleteFunc(slices.Clone(feeds), func(f *feed) bool { return f.genre != g })
			if err := m.take(ctx, group, min(len(m.out)+o.minPerGenre, o.limit)); err != nil {
				return nil, err
			}
		}
	}
	if err := m.take(ctx, feeds, o.limit); err != nil {
		return nil, err
	}
	if o.tracksOf != nil {
		for short := o.limit - m.tracks(); short > 0; short = o.limit - m.tracks() {
			before := len(m.out)
			if err := m.take(ctx, feeds, before+short); err != nil {
				return nil, err
			}
			if len(m.out) == before {
				break // every list is used up
			}
		}
	}
	var errs []error
	for _, f := range feeds {
		if f.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.name, f.err))
		}
	}
	if len(feeds) > 0 && len(errs) == len(feeds) {
		return nil, errors.Join(errs...)
	}
	// The releases a genre got first stand where their date puts them.
	order := make([]int, len(m.out))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return strings.Compare(m.dates[b], m.dates[a]) })
	out := make([]hardstyle.Track, len(order))
	for i, j := range order {
		out[i] = m.out[j]
	}
	return out, nil
}

// fromHardtunes tells a release of hardtunes.com: its page address is absolute, those of hardstyle.com are not.
func fromHardtunes(t hardstyle.Track) bool {
	return strings.HasPrefix(t.Path, "http://") || strings.HasPrefix(t.Path, "https://")
}

// releaseKey names a release the same on both sites: the artists in any order and with any joiner ("&",
// "and", ","), the title without format words, and the version in one spelling. Two versions of a song
// ("Extended Mix", "Radio Edit") are two releases.
func releaseKey(t hardstyle.Track) string {
	artists := match.SplitArtists(t.Artist)
	slices.Sort(artists)
	return strings.Join(artists, ",") + "|" + match.NormTitle(t.Title) + "|" + canonicalVersion(t.Version)
}

// versionSpellings are the ways the two sites write the same version. The original is often not named at all.
var versionSpellings = map[string]string{
	"original": "", "original mix": "", "original version": "",
	"extended": "extended", "extended mix": "extended", "extended version": "extended", "extended edit": "extended",
	"radio": "radio", "radio edit": "radio", "radio mix": "radio", "radio version": "radio",
}

// canonicalVersion writes a version line in one spelling: "Extended" and "Extended Mix" are the same version.
func canonicalVersion(v string) string {
	n := match.NormName(v)
	if c, ok := versionSpellings[n]; ok {
		return c
	}
	return n
}
