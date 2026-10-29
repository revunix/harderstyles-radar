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
	"strings"
	"time"
)

// Options are the settings of a run.
type Options struct {
	MaxTracks     int  // limit of the playlist
	MaxSearches   int  // limit of Spotify lookups per run; the rest waits for the next run
	DateTolerance int  // days the release date on Spotify may differ from hardstyle.com
	DryRun        bool // plan only, write nothing
	// KeepSiteOrder keeps the order of the list of hardstyle.com exactly, as it is with one list (the general one
	// or one genre). With several genres the lists are merged by release date.
	KeepSiteOrder bool
	// FetchDetails reads the release date and the ISRC of a release from its page on hardstyle.com. Without it
	// neither the date nor the ISRC is used.
	FetchDetails func(context.Context, hardstyle.Track) (hardstyle.Details, error)
	// AlbumMinTracks is the number of releases with the same catalog number on the site from which they count as
	// an album: all its tracks are then taken from the same Spotify album. 0 switches it off.
	AlbumMinTracks int
}

type Result struct {
	Scraped   int
	Matched   int
	Added     int
	Removed   int
	Deferred  int  // releases that wait for the next run because of the search limit
	Reordered bool // the order of the playlist had to be changed
	Before    int  // tracks in the playlist before the run
	After     int  // tracks in the playlist after the run
	Unmatched []hardstyle.Track
}

// resolved is what a run knows about one release.
type resolved struct {
	uris      []string // the Spotify tracks: the best match first, then more versions (remixes) of the song
	album     bool     // taken from the Spotify album of its catalog number
	albumName string   // the name of that album, if known
	date      string   // release date on hardstyle.com
	reason    string   // why there is no track: spotify.ReasonNotFound or spotify.ReasonDate
	spDate    string   // release date of the Spotify track that was too far off (spotify.ReasonDate)
	daysOff   int      // the match has the exact title and artist, but its date is this many days off
	why       string   // why the release waits (deferred)
	deferred  bool
}

// resolvedFrom is what the cache knows about a release.
func resolvedFrom(e cache.Entry, ok bool) resolved {
	r := resolved{date: e.Date, album: e.Album, daysOff: e.DaysOff}
	if e.URI != "" {
		r.uris = append([]string{e.URI}, e.Extra...)
	}
	if ok && e.URI == "" {
		r.reason, r.spDate = normalizeReason(e.Reason), e.SpotifyDate
	}
	return r
}

// primary is the best Spotify track of the release, "" if there is none.
func (r resolved) primary() string {
	if len(r.uris) == 0 {
		return ""
	}
	return r.uris[0]
}

// note says in words why a release is missing or waits; "" if it is fine.
func (r resolved) note() string {
	switch {
	case r.deferred:
		return r.why
	case r.album:
		return albumNote(r.albumName)
	case r.daysOff > 0:
		return fmt.Sprintf(i18n.T("Datum weicht um %d Tage ab, trotzdem aufgenommen", "Date is %d days off, taken anyway"), r.daysOff)
	case len(r.uris) > 1:
		return fmt.Sprintf(i18n.T("+%d weitere Version(en), z. B. Remixe", "+%d more version(s), e.g. remixes"), len(r.uris)-1)
	case len(r.uris) > 0:
		return ""
	}
	return reasonText(r.reason, r.spDate, r.date)
}

// reasonText turns the reason of a missing release into a text in the language of the moment.
func reasonText(reason, spotifyDate, siteDate string) string {
	if reason == spotify.ReasonDate && spotifyDate == "" { // an entry of an older version has no date of the Spotify track
		return fmt.Sprintf(i18n.T("Datum weicht ab: hardstyle.com %s", "Date differs: hardstyle.com %s"), siteDate)
	}
	if reason == spotify.ReasonDate {
		if _, diff := match.Date(siteDate, spotifyDate, "day", 0); diff > 0 {
			return fmt.Sprintf(i18n.T("Datum weicht ab: Spotify %s, hardstyle.com %s (%d Tage)", "Date differs: Spotify %s, hardstyle.com %s (%d days)"), spotifyDate, siteDate, diff)
		}
		return fmt.Sprintf(i18n.T("Datum weicht ab: Spotify %s, hardstyle.com %s", "Date differs: Spotify %s, hardstyle.com %s"), spotifyDate, siteDate)
	}
	return i18n.T("nicht bei Spotify gefunden", "not found on Spotify")
}

// normalizeReason reads a reason from the cache. Older versions saved the German text.
func normalizeReason(reason string) string {
	if reason == spotify.ReasonDate || strings.HasPrefix(reason, "Datum") || strings.HasPrefix(reason, "Date differs") {
		return spotify.ReasonDate
	}
	return spotify.ReasonNotFound
}

// Sync brings the playlist in line with hardstyle.com: the tracks of the latest releases, in the order of the site
// (newest first), at most MaxTracks, the oldest at the end drop out. To keep the number of Spotify requests small
// it uses the cache for everything it already knows, looks up at most MaxSearches releases, and changes the
// playlist with as few requests as possible.
func Sync(ctx context.Context, sp *spotify.Client, playlistID string, releases []hardstyle.Track, opt Options, c *cache.Cache, emit event.Emit) (Result, error) {
	res := Result{Scraped: len(releases)}
	defer func() {
		if err := c.Save(); err != nil {
			emit.Logf(i18n.T("Zwischenspeicher nicht gespeichert: %v", "Cache not saved: %v"), err)
		}
	}()

	current, err := loadPlaylist(ctx, sp, playlistID, c, emit)
	if err != nil {
		return res, fmt.Errorf(i18n.T("Playlist lesen: %w", "reading the playlist: %w"), err)
	}
	res.Before, res.After = len(current), len(current)
	inPlaylist := make(map[string]bool, len(current))
	for _, u := range current {
		inPlaylist[u] = true
	}
	emit.Send(event.Event{Kind: event.EvPlaylist, Playlist: event.StatsFor(current, c.DatesByURI())})
	emit.Send(event.Event{Kind: event.EvReleases, Releases: releases})
	emit.Send(event.Event{Kind: event.EvPhase, Phase: event.PhaseSearch, Done: 0, Total: len(releases)})

	known, stopErr := resolveAll(ctx, sp, releases, inPlaylist, opt, c, emit)
	if known == nil {
		return res, stopErr // cancelled
	}
	if stopErr != nil {
		emit.Logf(i18n.T("Die Suche wurde vorzeitig beendet: %v. Was gefunden wurde, kommt trotzdem in die Playlist.", "The search ended early: %v. What was found still goes into the playlist."), stopErr)
	}
	for i, r := range known {
		switch {
		case r.deferred:
			res.Deferred++
		case len(r.uris) == 0:
			res.Unmatched = append(res.Unmatched, releases[i])
		default:
			res.Matched++
		}
	}

	final := desiredOrder(known, current, opt.MaxTracks, opt.KeepSiteOrder)
	res.After = len(final)
	finalSet := setOf(final)
	for _, u := range final {
		if !inPlaylist[u] {
			res.Added++
		}
	}
	for _, u := range current {
		if !finalSet[u] {
			res.Removed++
		}
	}

	p := makePlan(current, final)
	res.Reordered = p.kind == planRewrite
	switch {
	case p.kind == planNone:
		emit.Logf("%s", i18n.T("Die Playlist ist schon richtig, nichts zu schreiben", "The playlist is already right, nothing to write"))
		return res, stopErr
	case opt.DryRun:
		emit.Logf(i18n.T("Testlauf: %s, nichts geschrieben", "Dry run: %s, nothing written"), p.describe(len(final)))
		return res, stopErr
	}

	emit.Send(event.Event{Kind: event.EvPhase, Phase: event.PhaseWrite, Done: 0, Total: 1})
	snap, err := p.apply(ctx, sp, playlistID)
	if err != nil {
		return res, fmt.Errorf(i18n.T("Playlist schreiben: %w", "writing the playlist: %w"), err)
	}
	emit.Logf("%s", p.describe(len(final)))
	emit.Send(event.Event{Kind: event.EvPhase, Phase: event.PhaseWrite, Done: 1, Total: 1})

	// The cache now holds exactly what was written, so the next run needs no read of the playlist: Spotify's
	// snapshot id tells whether somebody changed it in between.
	c.SetPlaylist(cache.PlaylistSnapshot{ID: playlistID, SnapshotID: snap, URIs: final, At: time.Now()})
	for i, r := range known {
		if slices.ContainsFunc(r.uris, func(u string) bool { return !inPlaylist[u] && finalSet[u] }) {
			emit.Send(event.Event{Kind: event.EvItem, Index: i, Status: event.StatusAdded, URI: r.primary(), Date: r.date, Note: r.note()})
		}
	}
	emit.Send(event.Event{Kind: event.EvPlaylist, Playlist: event.StatsFor(final, c.DatesByURI())})
	return res, stopErr
}

// loadPlaylist returns the tracks of the playlist in order. One cheap request (the snapshot id) tells whether
// the remembered tracks are still right; only then the whole playlist is read again.
func loadPlaylist(ctx context.Context, sp *spotify.Client, playlistID string, c *cache.Cache, emit event.Emit) ([]string, error) {
	snap, err := sp.PlaylistSnapshotID(ctx, playlistID)
	var limit *spotify.RateLimitError
	if errors.As(err, &limit) {
		return nil, err
	}
	if err != nil {
		emit.Logf(i18n.T("Snapshot der Playlist nicht lesbar (%v), lese sie ganz", "Playlist snapshot not readable (%v), reading it completely"), err)
		snap = ""
	}
	if c := c.Playlist(playlistID); c != nil && snap != "" && c.SnapshotID == snap && time.Since(c.At) < cache.PlaylistVerifyEvery {
		emit.Logf(i18n.T("Playlist unverändert, %d Tracks aus dem Zwischenspeicher (1 Anfrage)", "Playlist unchanged, %d tracks from the cache (1 request)"), len(c.URIs))
		return c.URIs, nil
	}
	entries, err := sp.PlaylistEntries(ctx, playlistID)
	if err != nil {
		return nil, err
	}
	uris := make([]string, len(entries))
	for i, e := range entries {
		uris[i] = e.URI
	}
	c.SetPlaylist(cache.PlaylistSnapshot{ID: playlistID, SnapshotID: snap, URIs: uris, At: time.Now()})
	emit.Logf(i18n.T("Playlist gelesen: %d Tracks", "Playlist read: %d tracks"), len(uris))
	return uris, nil
}

// resolveAll finds the Spotify track of every release. What the cache knows costs no request. Of the rest at
// most MaxSearches are looked up, the releases never seen before first, then the missing ones that are due
// again, the oldest check first. The others wait for the next run.
// maxLookupFailures is the number of lookups in a row that may fail before the run stops searching.
const maxLookupFailures = 5

// resolveAll finds the Spotify track of every release. What the cache knows costs no request. Of the rest at
// most MaxSearches are looked up, the releases never seen before first, then the missing ones that are due
// again, the oldest check first. The others wait for the next run.
//
// A lookup that fails does not end the run: the release waits for the next one. Only a rate limit or too many
// failures in a row stop the searching; the releases found until then are still returned, together with the
// error, so the caller can write them.
func resolveAll(ctx context.Context, sp *spotify.Client, releases []hardstyle.Track, inPlaylist map[string]bool, opt Options, c *cache.Cache, emit event.Emit) ([]resolved, error) {
	known := make([]resolved, len(releases))
	var fresh, due []int
	for i, tr := range releases {
		e, ok := c.Entry(tr)
		known[i] = resolvedFrom(e, ok)
		if a, found := c.Album(e.AlbumID); e.Album && found {
			known[i].albumName = a.Name
		}
		switch {
		case ok && e.Usable():
		case ok && !e.At.IsZero():
			due = append(due, i)
		default:
			fresh = append(fresh, i)
		}
	}
	sort.SliceStable(due, func(a, b int) bool {
		ea, _ := c.Entry(releases[due[a]])
		eb, _ := c.Entry(releases[due[b]])
		return ea.At.Before(eb.At)
	})
	todo := append(fresh, due...)
	if len(todo) > opt.MaxSearches {
		for _, i := range todo[opt.MaxSearches:] {
			known[i].deferred, known[i].why = true, i18n.T("wartet auf den nächsten Lauf (Suchlimit)", "waits for the next run (search limit)")
		}
		todo = todo[:opt.MaxSearches]
	}
	toLookup := make(map[int]bool, len(todo))
	for _, i := range todo {
		toLookup[i] = true
	}

	// The pages on hardstyle.com give the date, the ISRC and the catalog number. They are read for the releases
	// to look up, and for at most MaxSearches others whose catalog number is not known yet, so the albums in the
	// list are known.
	fetched := map[int]bool{}
	if opt.FetchDetails != nil {
		others := 0
		for i, tr := range releases {
			if e, _ := c.Entry(tr); !e.DetailsDue() {
				continue
			}
			if !toLookup[i] {
				if others >= opt.MaxSearches {
					continue
				}
				others++
			}
			d, err := opt.FetchDetails(ctx, tr)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil {
				continue // the search tries again
			}
			c.SetDetails(tr, d)
			known[i].date, fetched[i] = d.Date, true
		}
	}

	// A track of an album found before the album check existed does not know its Spotify album. It is looked up
	// once more, as long as the search limit has room.
	again := 0
	for _, g := range groupsOf(releases, func(i int) string { e, _ := c.Entry(releases[i]); return e.Catalog }, opt.AlbumMinTracks) {
		for _, i := range g.members {
			if e, _ := c.Entry(releases[i]); e.URI != "" && e.AlbumID == "" && !toLookup[i] && len(todo) < opt.MaxSearches {
				todo, toLookup[i] = append(todo, i), true
				again++
			}
		}
	}
	if again > 0 {
		emit.Logf(i18n.T("%d Album-Tracks werden einmal neu gesucht, um ihr Spotify-Album zu kennen", "%d album tracks are searched once more to know their Spotify album"), again)
	}

	cached := len(releases) - len(fresh) - len(due)
	emit.Logf(i18n.T("%d Releases: %d aus dem Zwischenspeicher, %d zu suchen (%d neu, %d erneut), %d warten auf den nächsten Lauf (Limit %d)", "%d releases: %d from the cache, %d to search (%d new, %d again), %d wait for the next run (limit %d)"),
		len(releases), cached, len(todo)-again, len(fresh), len(due), len(fresh)+len(due)-(len(todo)-again), opt.MaxSearches)

	reported := make([]bool, len(releases))
	done := 0
	item := func(i int) {
		r := known[i]
		emit.Send(event.Event{Kind: event.EvItem, Index: i, Status: statusOf(r, inPlaylist), URI: r.primary(), Date: r.date, Note: r.note()})
	}
	report := func(i int) {
		done++
		reported[i] = true
		item(i)
		emit.Send(event.Event{Kind: event.EvPhase, Phase: event.PhaseSearch, Done: done, Total: len(releases), Msg: releases[i].Title})
	}
	for i := range releases {
		if !toLookup[i] && !known[i].deferred {
			report(i)
		}
	}
	// finish reports what is left over: the releases that wait, and the ones nobody could find.
	finish := func() {
		for i := range releases {
			if !reported[i] {
				report(i)
			}
		}
		for i, r := range known {
			if len(r.uris) == 0 && !r.deferred {
				emit.Logf(i18n.T("Nicht bei Spotify: %s – %s (%s)", "Not on Spotify: %s – %s (%s)"), releases[i].Title, releases[i].Artist, r.note())
			}
		}
		for i, r := range known {
			if toLookup[i] && len(r.uris) > 0 && r.daysOff > 0 {
				emit.Logf(i18n.T("Datum weicht um %d Tage ab, trotzdem aufgenommen: %s – %s", "Date is %d days off, taken anyway: %s – %s"), r.daysOff, releases[i].Title, releases[i].Artist)
			}
		}
	}
	// stop gives up the rest of the lookups: those releases wait for the next run.
	stop := func(rest []int, why string) {
		for _, i := range rest {
			known[i].deferred, known[i].why = true, why
		}
	}

	cands := map[int][]spotify.Candidate{}
	failures := 0
	for n, i := range todo {
		tr := releases[i]
		e, _ := c.Entry(tr)
		details := hardstyle.Details{Date: known[i].date, ISRC: e.ISRC}
		// The page of the release is read once for its date and ISRC; again for a missing one without an ISRC.
		if opt.FetchDetails != nil && !fetched[i] && (e.DetailsDue() || (e.ISRC == "" && e.URI == "")) {
			if d, err := opt.FetchDetails(ctx, tr); err == nil {
				c.SetDetails(tr, d)
				known[i].date, details = d.Date, d
			} else if ctx.Err() != nil {
				return nil, ctx.Err()
			} else {
				emit.Logf(i18n.T("Seite von %q nicht lesbar (%v), Suche ohne Datum und ISRC", "Page of %q not readable (%v), searching without date and ISRC"), tr.Title, err)
			}
		}
		look, err := sp.FindTrack(ctx, tr, details, opt.DateTolerance)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			stopErr := fmt.Errorf(i18n.T("Suche nach %q: %w", "search for %q: %w"), tr.Title, err)
			var limit *spotify.RateLimitError
			failures++
			switch {
			case errors.As(err, &limit):
				stop(todo[n:], i18n.T("Spotify-Sperre, wartet auf den nächsten Lauf", "Spotify block, waits for the next run"))
			case failures >= maxLookupFailures:
				stopErr = fmt.Errorf(i18n.T("%d Suchen hintereinander fehlgeschlagen, zuletzt %w", "%d searches in a row failed, last: %w"), failures, stopErr)
				stop(todo[n:], i18n.T("Suche fehlgeschlagen, wartet auf den nächsten Lauf", "search failed, waits for the next run"))
			default:
				emit.Logf(i18n.T("%v, sie wird beim nächsten Lauf wiederholt", "%v, it is repeated at the next run"), stopErr)
				stop(todo[n:n+1], i18n.T("Suche fehlgeschlagen, wartet auf den nächsten Lauf", "search failed, waits for the next run"))
				report(i)
				continue
			}
			finish()
			return known, stopErr
		}
		failures = 0
		cands[i] = look.Candidates
		if len(look.URIs) > 0 {
			c.SetFound(tr, cache.Found{URIs: look.URIs, SpotifyDate: look.SpotifyDate, AlbumID: albumOf(look), DaysOff: look.DaysOff})
		} else {
			c.SetMissing(tr, look.Reason, look.SpotifyDate)
		}
		known[i].uris, known[i].album, known[i].albumName, known[i].reason, known[i].spDate, known[i].daysOff = look.URIs, false, "", look.Reason, look.SpotifyDate, look.DaysOff
		report(i)
	}

	// The tracks of an album come from the same Spotify album.
	changed, err := checkAlbums(ctx, sp, releases, known, cands, opt, c, emit)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for _, i := range changed {
		if reported[i] {
			item(i)
		}
	}
	finish()
	if err != nil {
		return known, fmt.Errorf(i18n.T("Albumprüfung: %w", "album check: %w"), err)
	}
	return known, nil
}

// albumOf returns the Spotify album of the best match of a lookup.
func albumOf(look spotify.Lookup) string {
	if len(look.URIs) == 0 {
		return ""
	}
	for _, cd := range look.Candidates {
		if cd.Track.URI == look.URIs[0] {
			return cd.Track.Album.ID
		}
	}
	return ""
}

func statusOf(r resolved, inPlaylist map[string]bool) event.Status {
	switch {
	case r.deferred:
		return event.StatusPending
	case len(r.uris) == 0 && r.reason == spotify.ReasonDate:
		return event.StatusMismatch
	case len(r.uris) == 0:
		return event.StatusNotFound
	case !slices.ContainsFunc(r.uris, func(u string) bool { return !inPlaylist[u] }):
		return event.StatusInPlaylist
	}
	return event.StatusNew
}

func setOf(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

// desiredOrder returns the playlist as it should be: the found tracks in the order of hardstyle.com (newest
// release date first, then the order of the site's list), then the tracks that are in the playlist but not among
// the latest releases any more, in the order they have. At most max tracks; the end drops out.
func desiredOrder(known []resolved, current []string, limit int, keepSiteOrder bool) []string {
	type entry struct {
		uris  []string
		date  string
		index int
	}
	var found []entry
	for i, r := range known {
		if len(r.uris) > 0 {
			found = append(found, entry{r.uris, r.date, i})
		}
	}
	sort.SliceStable(found, func(a, b int) bool {
		x, y := found[a], found[b]
		if !keepSiteOrder && x.date != y.date {
			return y.date == "" || (x.date != "" && x.date > y.date) // unknown dates last
		}
		return x.index < y.index
	})
	final := make([]string, 0, len(found)+len(current))
	seen := map[string]bool{}
	for _, f := range found {
		for _, u := range f.uris {
			if !seen[u] {
				seen[u] = true
				final = append(final, u)
			}
		}
	}
	for _, u := range current {
		if !seen[u] {
			final = append(final, u)
		}
	}
	if len(final) > limit {
		final = final[:limit]
	}
	return final
}

type planKind int

const (
	planNone        planKind = iota
	planIncremental          // put new tracks at the top, take the end off
	planRewrite              // write the whole playlist anew
)

// plan is the cheapest way to turn the playlist into the wanted one.
type plan struct {
	kind       planKind
	head, tail []string // incremental: tracks to put at the top, tracks to remove
	final      []string
}

// makePlan compares the playlist with the wanted order. The usual day only brings new tracks at the top and
// pushes old ones out at the end: that needs one insert and one remove. Any other difference (the order was
// wrong) needs the playlist written anew, which are only a few requests of 100 tracks.
func makePlan(current, wanted []string) plan {
	if slices.Equal(current, wanted) {
		return plan{kind: planNone}
	}
	inCurrent := setOf(current)
	for m := 0; m <= len(wanted); m++ {
		rest := wanted[m:]
		if len(rest) > len(current) || !slices.Equal(rest, current[:len(rest)]) {
			continue
		}
		head := wanted[:m]
		if slices.ContainsFunc(head, func(u string) bool { return inCurrent[u] }) {
			continue
		}
		return plan{kind: planIncremental, head: head, tail: current[len(rest):], final: wanted}
	}
	return plan{kind: planRewrite, final: wanted}
}

func (p plan) describe(total int) string {
	if p.kind == planRewrite {
		return fmt.Sprintf(i18n.T("Playlist neu sortiert und geschrieben (%d Tracks)", "Playlist re-sorted and rewritten (%d tracks)"), total)
	}
	return fmt.Sprintf(i18n.T("%d Tracks oben eingefügt, %d am Ende entfernt (%d Tracks)", "%d tracks inserted at the top, %d removed at the end (%d tracks)"), len(p.head), len(p.tail), total)
}

// apply writes the plan and returns the snapshot id of the playlist afterwards.
func (p plan) apply(ctx context.Context, sp *spotify.Client, playlistID string) (string, error) {
	if p.kind == planRewrite {
		return sp.ReplaceTracks(ctx, playlistID, p.final)
	}
	snap := ""
	var err error
	// Insert before removing, so a failure never loses tracks.
	if len(p.head) > 0 {
		if snap, err = sp.InsertAtTop(ctx, playlistID, p.head); err != nil {
			return "", err
		}
	}
	if len(p.tail) > 0 {
		if snap, err = sp.RemoveTracks(ctx, playlistID, p.tail); err != nil {
			return "", err
		}
	}
	return snap, nil
}
