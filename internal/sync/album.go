package sync

import (
	"context"
	"errors"
	"fmt"
	"hardstylesync/internal/cache"
	"hardstylesync/internal/event"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/match"
	"hardstylesync/internal/spotify"
	"slices"
	"sort"
)

// An album on hardstyle.com is a run of releases with the same catalog number ("MINUS288"), whoever the artist.
// Each of them is looked up on its own, so one may find the single and the next the album. The album check
// finds the Spotify album the releases have in common and takes every track from it.

// albumGroup is a set of releases of the list with the same catalog number.
type albumGroup struct {
	catalog string
	members []int // positions in the list, in order
}

// groupsOf returns the albums in the list: the catalog numbers with at least minTracks releases. 0 for minTracks
// switches it off.
func groupsOf(releases []hardstyle.Track, catalogOf func(int) string, minTracks int) []albumGroup {
	if minTracks <= 0 {
		return nil
	}
	byCatalog := map[string][]int{}
	var order []string
	for i := range releases {
		cat := catalogOf(i)
		if cat == "" {
			continue
		}
		if _, ok := byCatalog[cat]; !ok {
			order = append(order, cat)
		}
		byCatalog[cat] = append(byCatalog[cat], i)
	}
	var out []albumGroup
	for _, cat := range order {
		if len(byCatalog[cat]) >= max(minTracks, 2) {
			out = append(out, albumGroup{cat, byCatalog[cat]})
		}
	}
	return out
}

// albumVote is what the members of a group say about one Spotify album.
type albumVote struct {
	album spotify.Album
	date  string // release date of the album
	votes int
}

// pickAlbum finds the Spotify album of a group: the album that has the most of its releases. Each member votes
// once for every album its search found it on; a member taken from the cache votes for the album of its match.
// The album must have at least two of them and at least half of the members that voted, and its date must be
// near the date on hardstyle.com: an older album of the artist with some of the same songs is not it. On a tie
// an album beats a single, then the album with more tracks, then the nearer date.
func pickAlbum(g albumGroup, known []resolved, cands map[int][]spotify.Candidate, albumOf func(int) string, siteDate string, c *cache.Cache) (albumVote, bool) {
	votes := map[string]*albumVote{}
	voters := 0
	for _, m := range g.members {
		seen := map[string]bool{}
		add := func(id string, a spotify.Album, date string) {
			if id == "" || seen[id] {
				return
			}
			seen[id] = true
			v := votes[id]
			if v == nil {
				v = &albumVote{album: spotify.Album{ID: id}}
				votes[id] = v
			}
			if a.ID != "" {
				v.album, v.date = a, date
			}
			v.votes++
		}
		if cs, ok := cands[m]; ok {
			for _, cd := range cs {
				add(cd.Track.Album.ID, cd.Track.Album, cd.Track.ReleaseDate)
			}
		} else if len(known[m].uris) > 0 {
			add(albumOf(m), spotify.Album{}, "")
		}
		if len(seen) > 0 {
			voters++
		}
	}
	var all []*albumVote
	for id, v := range votes {
		if v.date == "" {
			if a, ok := c.Album(id); ok {
				v.date = a.Date
				if v.album.Name == "" {
					v.album.Name = a.Name
				}
			}
		}
		all = append(all, v)
	}
	isSingle := func(v *albumVote) bool { return v.album.Type == "single" }
	daysOff := func(v *albumVote) int {
		_, diff := match.Date(siteDate, v.date, precisionOf(v.date), 0)
		return diff
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		switch {
		case a.votes != b.votes:
			return a.votes > b.votes
		case isSingle(a) != isSingle(b):
			return !isSingle(a)
		case a.album.Tracks != b.album.Tracks:
			return a.album.Tracks > b.album.Tracks
		case daysOff(a) != daysOff(b):
			return daysOff(a) < daysOff(b)
		}
		return a.album.ID < b.album.ID
	})
	for _, v := range all {
		if v.votes < 2 || v.votes*2 < voters {
			return albumVote{}, false
		}
		if daysOff(v) <= match.GraceDays {
			return *v, true
		}
	}
	return albumVote{}, false
}

// precisionOf tells how exact a date of Spotify is: "2026-10-29", "2026-10" or "2026".
func precisionOf(date string) string {
	switch len(date) {
	case 4:
		return "year"
	case 7:
		return "month"
	}
	return "day"
}

// albumTracks returns the tracks of the album: from the cache, else from Spotify. When Spotify does not give
// them, the tracks the searches found on it have to do.
func albumTracks(ctx context.Context, sp *spotify.Client, v albumVote, cands map[int][]spotify.Candidate, c *cache.Cache) ([]cache.AlbumTrack, bool, error) {
	if a, ok := c.Album(v.album.ID); ok {
		return a.Tracks, false, nil
	}
	list, err := sp.AlbumTracks(ctx, v.album.ID)
	if err == nil {
		var tracks []cache.AlbumTrack
		for _, t := range list {
			tracks = append(tracks, cache.AlbumTrack{URI: t.URI, Name: t.Name, Artists: t.Artists})
		}
		c.SetAlbum(v.album.ID, cache.AlbumEntry{Name: v.album.Name, Date: v.date, Tracks: tracks})
		return tracks, true, nil
	}
	var tracks []cache.AlbumTrack
	seen := map[string]bool{}
	for _, cs := range cands {
		for _, cd := range cs {
			if cd.Track.Album.ID == v.album.ID && !seen[cd.Track.URI] {
				seen[cd.Track.URI] = true
				tracks = append(tracks, cache.AlbumTrack{URI: cd.Track.URI, Name: cd.Track.Name, Artists: cd.Track.Artists})
			}
		}
	}
	return tracks, true, err
}

// findOnAlbum returns the track of the album that is the release: the same title (with its version, else
// without it) and an artist in common. Tracks already given to another release are left out.
func findOnAlbum(tr hardstyle.Track, tracks []cache.AlbumTrack, taken map[string]bool) (cache.AlbumTrack, bool) {
	best, bestKind := cache.AlbumTrack{}, match.None
	for _, t := range tracks {
		if taken[t.URI] || !match.Artists(tr.Artist, t.Artists) {
			continue
		}
		kind := match.Title(tr, t.Name)
		if (kind == match.Exact || kind == match.Base) && (bestKind == match.None || kind < bestKind) {
			best, bestKind = t, kind
		}
	}
	return best, bestKind != match.None
}

// checkAlbums takes the tracks of every album in the list from the same Spotify album. cands are the
// candidates of the releases looked up in this run. It returns the releases whose tracks changed.
func checkAlbums(ctx context.Context, sp *spotify.Client, releases []hardstyle.Track, known []resolved, cands map[int][]spotify.Candidate, opt Options, c *cache.Cache, emit event.Emit) ([]int, error) {
	entry := func(i int) cache.Entry {
		e, _ := c.Entry(releases[i])
		return e
	}
	groups := groupsOf(releases, func(i int) string { return entry(i).Catalog }, opt.AlbumMinTracks)
	var changed []int
	for _, g := range groups {
		siteDate := ""
		for _, m := range g.members {
			if siteDate = known[m].date; siteDate != "" {
				break
			}
		}
		v, ok := pickAlbum(g, known, cands, func(i int) string { return entry(i).AlbumID }, siteDate, c)
		if !ok {
			continue
		}
		// needs tells whether a member is not yet a track of the album.
		needs := func(m int) bool {
			return known[m].deferred || len(known[m].uris) == 0 || entry(m).AlbumID != v.album.ID
		}
		if !slices.ContainsFunc(g.members, needs) {
			continue // all from the album already: nothing to ask
		}
		tracks, fresh, err := albumTracks(ctx, sp, v, cands, c)
		if err != nil {
			if ctx.Err() != nil {
				return changed, ctx.Err()
			}
			var limit *spotify.RateLimitError
			if errors.As(err, &limit) {
				return changed, err
			}
			emit.Logf(i18n.T("Titelliste des Albums %q nicht lesbar (%v), nehme nur die Treffer der Suche", "Track list of the album %q not readable (%v), taking only the search results"), v.album.Name, err)
		}

		taken := map[string]bool{}
		for _, m := range g.members {
			if !needs(m) {
				taken[known[m].primary()] = true
			}
		}
		fromAlbum, notOnIt := 0, 0
		for _, m := range g.members {
			if !needs(m) {
				fromAlbum++
				continue
			}
			t, ok := findOnAlbum(releases[m], tracks, taken)
			if !ok {
				notOnIt++
				if fresh || cands[m] != nil {
					emit.Logf(i18n.T("Nicht im Spotify-Album %q: %s – %s", "Not on the Spotify album %q: %s – %s"), v.album.Name, releases[m].Title, releases[m].Artist)
				}
				continue
			}
			taken[t.URI] = true
			fromAlbum++
			known[m] = takeFromAlbum(known[m], t.URI, cands[m], v, opt.DateTolerance)
			c.SetFound(releases[m], cache.Found{URIs: known[m].uris, SpotifyDate: v.date, Album: true, AlbumID: v.album.ID, DaysOff: known[m].daysOff})
			changed = append(changed, m)
		}
		emit.Logf(i18n.T("Album %q (%s): %d von %d Tracks aus dem Album, %d nicht darauf", "Album %q (%s): %d of %d tracks from the album, %d not on it"),
			v.album.Name, g.catalog, fromAlbum, len(g.members), notOnIt)
	}
	return changed, nil
}

// takeFromAlbum makes the track of the album the match of a release. The recording it had before (the single)
// goes, the other versions of the song (remixes) the search found stay.
func takeFromAlbum(r resolved, albumURI string, cands []spotify.Candidate, v albumVote, tolerance int) resolved {
	sameSong := map[string]bool{r.primary(): true}
	for _, cd := range cands {
		if cd.Kind == match.Exact || cd.Kind == match.Base {
			sameSong[cd.Track.URI] = true
		}
	}
	uris := []string{albumURI}
	for _, u := range r.uris {
		if !sameSong[u] && u != albumURI {
			uris = append(uris, u)
		}
	}
	r.uris, r.album, r.albumName, r.reason, r.spDate, r.deferred, r.why = uris, true, v.album.Name, "", v.date, false, ""
	r.daysOff = 0
	if _, diff := match.Date(r.date, v.date, precisionOf(v.date), 0); diff > tolerance {
		r.daysOff = diff
	}
	return r
}

// albumNote is the note of a release whose track was taken from the album.
func albumNote(name string) string {
	if name == "" {
		return i18n.T("aus dem Album der Veröffentlichung", "from the album of the release")
	}
	return fmt.Sprintf(i18n.T("aus dem Album %q", "from the album %q"), name)
}
