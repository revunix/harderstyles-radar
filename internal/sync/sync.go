package sync

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"hardstylesync/internal/cache"
	"hardstylesync/internal/config"
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
	// Order is how the playlist is sorted. Whatever it is, the newest MaxTracks tracks are kept.
	Order config.Order
	// FetchDetails reads the release date and the ISRC of a release from its page on hardstyle.com. Without it
	// neither the date nor the ISRC is used.
	FetchDetails func(context.Context, hardstyle.Track) (hardstyle.Details, error)
	// AlbumMinTracks is the number of releases with the same catalog number on the site from which they count as
	// an album: all its tracks are then taken from the same Spotify album. 0 switches it off.
	AlbumMinTracks int
	// EvalSamples is how many releases the ISRC finds in this run are also searched by title, for the
	// evaluation (see config.Config.EvalSamples).
	EvalSamples int
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
	// AddedTracks are the releases that got tracks into the playlist in this run, in the order of the site.
	AddedTracks []hardstyle.Track
	// Uncertain are the releases for which two different Spotify tracks fit almost equally well: none was
	// taken, a correction decides.
	Uncertain []hardstyle.Track
	// Warnings are what looked wrong in the run (a list not readable, a site that may have changed).
	Warnings []string
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
	if reason == spotify.ReasonUncertain {
		return i18n.T("unsicher: zwei Spotify-Tracks passen fast gleich gut – in corrections.txt festlegen", "uncertain: two Spotify tracks fit almost equally well – decide in corrections.txt")
	}
	return i18n.T("nicht bei Spotify gefunden", "not found on Spotify")
}

// normalizeReason reads a reason from the cache. Older versions saved the German text.
func normalizeReason(reason string) string {
	switch {
	case reason == spotify.ReasonUncertain:
		return reason
	case reason == spotify.ReasonDate || strings.HasPrefix(reason, "Datum") || strings.HasPrefix(reason, "Date differs"):
		return spotify.ReasonDate
	}
	return spotify.ReasonNotFound
}

// Sync brings the playlist in line with the sites: the tracks of the latest releases, at most MaxTracks, sorted
// as Order says; the oldest drop out. To keep the number of Spotify requests small it uses the cache for
// everything it already knows, looks up at most MaxSearches releases, and changes the playlist with as few
// requests as possible.
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
		case len(r.uris) == 0 && r.reason == spotify.ReasonUncertain:
			res.Uncertain = append(res.Uncertain, releases[i])
		case len(r.uris) == 0:
			res.Unmatched = append(res.Unmatched, releases[i])
		default:
			res.Matched++
		}
	}

	final := desiredOrder(known, releases, current, refusedURIs(releases, c), c.DatesByURI(), opt.MaxTracks, opt.Order)
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
			res.AddedTracks = append(res.AddedTracks, releases[i])
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

	// A release whose day has not come is not on Spotify yet: it is searched from its day on, and takes no
	// search of the limit before (when its day is known already).
	today := time.Now().Format("2006-01-02")
	todo := slices.DeleteFunc(slices.Concat(fresh, due), func(i int) bool {
		why, wait := notOutYet(known[i].date, today)
		if wait {
			known[i].deferred, known[i].why = true, why
		}
		return wait
	})
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
		// The pages are read one per second and the search has not started, so without a count of its own the
		// run looks stuck. The pages due are counted first, then reported as they are read.
		due := 0
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
			due++
		}
		read := 0
		others = 0
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
			read++
			emit.Send(event.Event{Kind: event.EvPhase, Phase: event.PhaseSearch, Done: read, Total: due, Msg: tr.Title})
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
	samples := opt.EvalSamples
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
		// The page may only now have named the day of the release, and it may not have come yet.
		if why, wait := notOutYet(known[i].date, today); wait {
			known[i].deferred, known[i].why = true, why
			report(i)
			continue
		}
		look, err := sp.FindTrack(ctx, tr, details, spotify.MatchOptions{
			Tolerance: opt.DateTolerance,
			ArtistIDs: c.ArtistIDs,
			Sample:    samples > 0 && details.ISRC != "",
		})
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
		// A correction can refuse a Spotify track for good. It drops out of this answer, and out of the
		// candidates the album check picks from, so it is not taken by another way.
		if len(e.Rejected) > 0 {
			refused := setOf(e.Rejected)
			look.URIs = slices.DeleteFunc(look.URIs, func(u string) bool { return refused[u] })
			look.Candidates = slices.DeleteFunc(look.Candidates, func(cd spotify.Candidate) bool { return refused[cd.Track.URI] })
			if len(look.URIs) == 0 {
				look.Reason = spotify.ReasonNotFound
			}
		}
		cands[i] = look.Candidates
		if look.Sampled {
			samples--
		}
		if len(look.Searched) > 0 {
			stored := make([]cache.StoredTrack, len(look.Searched))
			for k, t := range look.Searched {
				stored[k] = t.Stored()
			}
			c.SetSearched(tr, cache.StoredRelease{Title: tr.Title, Version: tr.Version, Artist: tr.Artist, Label: tr.Label,
				Duration: cmp.Or(details.Duration, tr.Duration), Date: details.Date, ISRC: details.ISRC}, stored)
		}
		if len(look.URIs) > 0 {
			c.SetFound(tr, cache.Found{URIs: look.URIs, SpotifyDate: look.SpotifyDate, AlbumID: albumOf(look), DaysOff: look.DaysOff, ByISRC: look.ByISRC})
			if look.ByISRC {
				if k := slices.IndexFunc(look.Candidates, func(cd spotify.Candidate) bool { return cd.Track.URI == look.URIs[0] }); k >= 0 {
					learnArtists(c, tr, look.Candidates[k].Track)
				}
			}
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
	case len(r.uris) == 0 && r.reason == spotify.ReasonUncertain:
		return event.StatusUncertain
	case len(r.uris) == 0:
		return event.StatusNotFound
	case !slices.ContainsFunc(r.uris, func(u string) bool { return !inPlaylist[u] }):
		return event.StatusInPlaylist
	}
	return event.StatusNew
}

// notOutYet tells a release whose day (yyyy-mm-dd) comes after today, and says so for the screen.
func notOutYet(date, today string) (why string, wait bool) {
	if date <= today {
		return "", false
	}
	return fmt.Sprintf(i18n.T("erscheint am %s, wird ab dann gesucht", "comes out on %s, searched from then on"), date), true
}

// learnArtists remembers which Spotify artist a site artist is, from a match the ISRC confirmed. The artists
// whose names fit pair up by name; when one site artist and one Spotify artist are left without a pair, they
// are the same artist under two names ("Smurf" and "DJ Smurf"), and the next search knows it.
func learnArtists(c *cache.Cache, t hardstyle.Track, sp spotify.Track) {
	used := map[int]bool{}
	var siteLeft []string
	for _, name := range match.SplitArtists(t.Artist) {
		k := -1
		for j, a := range sp.Artists {
			if !used[j] && match.Artists(name, []string{a}) {
				k = j
				break
			}
		}
		if k < 0 {
			siteLeft = append(siteLeft, name)
			continue
		}
		used[k] = true
		if k < len(sp.ArtistIDs) {
			c.LearnArtist(name, sp.ArtistIDs[k])
		}
	}
	var spLeft []int
	for k := range sp.Artists {
		if !used[k] {
			spLeft = append(spLeft, k)
		}
	}
	if len(siteLeft) == 1 && len(spLeft) == 1 && spLeft[0] < len(sp.ArtistIDs) {
		c.LearnArtist(siteLeft[0], sp.ArtistIDs[spLeft[0]])
	}
}

// refusedURIs collects the Spotify tracks a correction refused, over all releases of the run.
func refusedURIs(releases []hardstyle.Track, c *cache.Cache) []string {
	var out []string
	for _, t := range releases {
		e, _ := c.Entry(t)
		out = append(out, e.Rejected...)
	}
	return out
}

func setOf(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

// desiredOrder returns the playlist as it should be. First it decides which tracks stay: the newest, at most
// limit. Those are the found releases, newest release date first (equal dates in the order of the releases),
// then the tracks that are in the playlist but not among the releases any more, newest first as far as dates
// knows them (by Spotify URI), else in the order they stand. The oldest drop out. A track a correction refused
// is left out, even if it is in the playlist now. Then order decides where the kept tracks stand; the tracks of
// one release (its versions) stay together, and with artist or title the tracks only the playlist knows go
// last.
func desiredOrder(known []resolved, releases []hardstyle.Track, current, refused []string, dates map[string]string, limit int, order config.Order) []string {
	type group struct {
		uris    []string
		release *hardstyle.Track // nil for a track that is only in the playlist
	}
	newestFirst := func(x, y string) int { // unknown dates last
		switch {
		case x == y:
			return 0
		case x == "":
			return 1
		case y == "", x > y:
			return -1
		}
		return 1
	}
	seen := map[string]bool{}
	for _, u := range refused {
		// Marked as seen before anything is added, so the refused track is neither added nor kept.
		seen[u] = true
	}
	var found []int
	for i, r := range known {
		if len(r.uris) > 0 {
			found = append(found, i)
		}
	}
	slices.SortStableFunc(found, func(a, b int) int { return newestFirst(known[a].date, known[b].date) })
	var groups []group
	for _, i := range found {
		var uris []string
		for _, u := range known[i].uris {
			if !seen[u] {
				seen[u] = true
				uris = append(uris, u)
			}
		}
		if len(uris) > 0 {
			groups = append(groups, group{uris, &releases[i]})
		}
	}
	var rest []string
	for _, u := range current {
		if !seen[u] {
			seen[u] = true
			rest = append(rest, u)
		}
	}
	if order == config.OrderOldest {
		slices.Reverse(rest) // the playlist stands oldest first: its newest are at the end
	}
	slices.SortStableFunc(rest, func(a, b string) int { return newestFirst(dates[a], dates[b]) })
	for _, u := range rest {
		groups = append(groups, group{uris: []string{u}})
	}

	var kept []group
	for left := limit; left > 0 && len(groups) > 0; groups = groups[1:] {
		g := groups[0]
		if len(g.uris) > left {
			g.uris = g.uris[:left]
		}
		left -= len(g.uris)
		kept = append(kept, g)
	}

	switch order {
	case config.OrderOldest:
		slices.Reverse(kept)
	case config.OrderArtist, config.OrderTitle:
		slices.SortStableFunc(kept, func(a, b group) int {
			switch {
			case a.release == nil || b.release == nil:
				return cmpBool(a.release == nil, b.release == nil)
			case order == config.OrderArtist:
				return cmpKeys(a.release.Artist, b.release.Artist, a.release.Title, b.release.Title, a.release.Version, b.release.Version)
			}
			return cmpKeys(a.release.Title, b.release.Title, a.release.Artist, b.release.Artist, a.release.Version, b.release.Version)
		})
	}
	final := make([]string, 0, limit)
	for _, g := range kept {
		final = append(final, g.uris...)
	}
	return final
}

// cmpKeys compares names pair by pair, folded (accents, case, punctuation do not count), the first difference
// decides.
func cmpKeys(pairs ...string) int {
	for i := 0; i+1 < len(pairs); i += 2 {
		if c := strings.Compare(match.NormName(pairs[i]), match.NormName(pairs[i+1])); c != 0 {
			return c
		}
	}
	return 0
}

// cmpBool orders false before true.
func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
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
