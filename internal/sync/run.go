package sync

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"hardstylesync/internal/cache"
	"hardstylesync/internal/config"
	"hardstylesync/internal/corrections"
	"hardstylesync/internal/event"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/hardtunes"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/notify"
	"hardstylesync/internal/spotify"
	"log"
	"net/http"
	"slices"
	"strings"
	gosync "sync"
	"time"
)

// Run does one run and sends its summary to the Discord webhook when one is set.
func Run(ctx context.Context, hc *http.Client, sp *spotify.Client, cfg config.Config, dryRun, quick bool, emit event.Emit) (Result, error) {
	res, err := run(ctx, hc, sp, cfg, dryRun, quick, emit)
	notifyRun(ctx, hc, cfg, res, err, dryRun, quick, sp, emit)
	return res, err
}

// notifyRun sends the summary to Discord. A check for new tracks that found nothing stays quiet, unless its
// warnings differ from the last ones reported; a full run always reports, a failed run reports its error. A
// failed message is only logged: the run itself was fine.
func notifyRun(ctx context.Context, hc *http.Client, cfg config.Config, res Result, err error, dryRun, quick bool, sp *spotify.Client, emit event.Emit) {
	if cfg.DiscordWebhook == "" || dryRun {
		return
	}
	now := time.Now()
	var nerr error
	switch {
	case err != nil:
		if errors.Is(err, context.Canceled) {
			return
		}
		nerr = notify.SendFailure(ctx, hc, cfg.DiscordWebhook, now, err)
	case quick && res.Added == 0 && res.Removed == 0 && !warningsChanged(res.Warnings):
		return
	default:
		nerr = notify.SendRun(ctx, hc, cfg.DiscordWebhook, notify.RunSummary{
			PlaylistID: cfg.PlaylistID,
			At:         now,
			After:      res.After,
			Added:      res.Added,
			Removed:    res.Removed,
			Matched:    res.Matched,
			Missing:    len(res.Unmatched),
			Deferred:   res.Deferred,
			Requests:   sp.Requests(),
			NewTracks:  res.AddedTracks,
			Uncertain:  res.Uncertain,
			Warnings:   res.Warnings,
		})
	}
	if err == nil {
		rememberWarnings(res.Warnings)
	}
	if nerr != nil {
		emit.Logf(i18n.T("Discord-Nachricht nicht gesendet: %v", "Discord message not sent: %v"), nerr)
	}
}

// lastWarnings are the warnings of the last run of this process, so a broken list does not ring every few
// minutes: a quiet check reports only when its warnings changed.
var (
	warningsMu   gosync.Mutex
	lastWarnings []string
)

func warningsChanged(w []string) bool {
	warningsMu.Lock()
	defer warningsMu.Unlock()
	return !slices.Equal(w, lastWarnings)
}

func rememberWarnings(w []string) {
	warningsMu.Lock()
	defer warningsMu.Unlock()
	lastWarnings = slices.Clone(w)
}

// run does one run: read the releases from hardstyle.com and bring the playlist up to date.
//
// A quick run is the check for new tracks that a service makes every few minutes: it reads the list only as far
// as the first page that has a release it knows, and does nothing, not even one Spotify request, when there is
// nothing new. A full run reads all pages of the setting.
func run(ctx context.Context, hc *http.Client, sp *spotify.Client, cfg config.Config, dryRun, quick bool, emit event.Emit) (Result, error) {
	c, err := cache.Load(cfg.CacheFile)
	if err != nil {
		emit.Logf(i18n.T("Zwischenspeicher nicht lesbar: %v", "Cache not readable: %v"), err)
	}
	applyCorrections(cfg.CorrectionsFile, c, emit)

	// While Spotify blocks the app nothing is asked: every request could prolong the block. With fallback apps
	// that holds when all of them are blocked; the client starts with the first one that is not.
	if len(cfg.Credentials) > 0 {
		c.AdoptLegacyBlock(cfg.Credentials[0].ClientID)
	}
	var earliest time.Time
	free := len(cfg.Credentials) == 0
	for _, cr := range cfg.Credentials {
		until := c.BlockedUntil(cr.ClientID)
		sp.SetBlocked(cr.ClientID, until)
		if until.IsZero() {
			free = true
		} else if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	if !free {
		emit.Logf(i18n.T("Spotify sperrt die App noch bis %s, es wird nichts angefragt", "Spotify blocks the app until %s, nothing is requested"), i18n.Stamp(earliest))
		return Result{}, &spotify.RateLimitError{RetryAfter: time.Until(earliest), Until: earliest}
	}
	sp.SetEmitter(emit)
	sp.SetDelay(cfg.RequestDelay)
	sp.ResetRequests()
	sp.OnBlock = func(clientID string, until time.Time) {
		c.Block(clientID, until)
		_ = c.Save()
	}

	releases, err := readWindow(ctx, hc, cfg, quick, c, emit)
	if err != nil {
		return Result{}, err
	}
	if len(releases.tracks) == 0 {
		return Result{Warnings: releases.warnings}, errors.New(i18n.T("keine Releases auf hardstyle.com oder hardtunes.com gefunden", "no releases found on hardstyle.com or hardtunes.com"))
	}
	htByPath := releases.hardtunes

	if quick {
		fresh, due := countWork(releases.tracks, c)
		if fresh+due == 0 {
			emit.Logf(i18n.T("Keine neuen Tracks (%d geprüft), nichts zu tun", "No new tracks (%d checked), nothing to do"), len(releases.tracks))
			showFromCache(releases.tracks, cfg.PlaylistID, c, emit)
			emit.Send(event.Event{Kind: event.EvPhase, Phase: event.PhaseDone, Done: 1, Total: 1})
			return Result{Scraped: len(releases.tracks), Warnings: releases.warnings}, nil
		}
		emit.Logf(i18n.T("%d neue und %d erneut zu prüfende Tracks gefunden", "%d new and %d to check again found"), fresh, due)
	}

	hsDetails := hardstyle.NewDetailFetcher(hc, hardstyle.DetailDelay)
	htDetails := hardtunes.NewDetailFetcher(hc, hardtunes.DetailDelay)
	res, err := Sync(ctx, sp, cfg.PlaylistID, releases.tracks, Options{
		MaxTracks:      cfg.MaxTracks,
		MaxSearches:    cfg.MaxSearches,
		DateTolerance:  cfg.DateTolerance,
		AlbumMinTracks: cfg.AlbumMinTracks,
		DryRun:         dryRun,
		Order:          cfg.Order,
		EvalSamples:    cfg.EvalSamples,
		FetchDetails: func(ctx context.Context, tr hardstyle.Track) (hardstyle.Details, error) {
			if t, ok := htByPath[tr.Path]; ok {
				return htDetails.Fetch(ctx, t)
			}
			if releases.detailsRead[tr.Path] {
				// The window read this page a moment ago; what it said is in the cache.
				e, _ := c.Entry(tr)
				return hardstyle.Details{Date: e.Date, ISRC: e.ISRC, Catalog: e.Catalog}, nil
			}
			return hsDetails.Fetch(ctx, tr)
		},
	}, c, emit)
	res.Warnings = append(releases.warnings, res.Warnings...)
	if err != nil {
		return res, err
	}
	if !dryRun {
		updateDescription(ctx, sp, cfg, c, time.Now(), emit)
	}
	emit.Send(event.Event{Kind: event.EvPhase, Phase: event.PhaseDone, Done: 1, Total: 1})
	emit.Logf(i18n.T("Fertig: %d gelesen, %d bei Spotify, %d neu in der Playlist, %d entfernt, %d fehlen, %d warten, %d Spotify-Anfragen", "Done: %d read, %d found on Spotify, %d new in the playlist, %d removed, %d missing, %d waiting, %d Spotify requests"),
		res.Scraped, res.Matched, res.Added, res.Removed, len(res.Unmatched), res.Deferred, sp.Requests())
	return res, nil
}

// window is what a run reads from the sites: the newest MaxTracks releases, the hardtunes.com releases among
// them by page address, so their details are read from the right site, the hardstyle.com release pages read
// for their dates, so they are not read twice, and what looked wrong on the way (warnings).
type window struct {
	tracks      []hardstyle.Track
	hardtunes   map[string]hardtunes.Track
	detailsRead map[string]bool
	warnings    []string
}

// readWindow reads the newest MaxTracks releases of all lists of both sites, newest first, and no page more:
// the lists are read page by page, only as far as the newest releases reach. A new release pushes the oldest
// out of the window, like it pushes the oldest track out of the playlist. The release dates of hardstyle.com
// are on its release pages; they are read once and kept in the cache. A check for new tracks (quick) reads a
// list only up to the first page with a release it knows. A list that cannot be read is left out of this run
// with a warning; the run fails only when no list can be read.
func readWindow(ctx context.Context, hc *http.Client, cfg config.Config, quick bool, c *cache.Cache, emit event.Emit) (window, error) {
	w := window{hardtunes: map[string]hardtunes.Track{}, detailsRead: map[string]bool{}}
	var stopAt func(hardstyle.Track) bool
	if quick {
		stopAt = func(t hardstyle.Track) bool {
			e, ok := c.Entry(t)
			return ok && !e.At.IsZero()
		}
	}
	var feeds []*feed
	for _, genre := range cfg.Genres {
		feeds = append(feeds, &feed{
			name:     "hardstyle.com " + hardstyle.GenreLabel(genre),
			genre:    hardstyle.GenreLabel(genre),
			delay:    hardstyle.PageDelay,
			maxPages: cfg.Pages,
			stopAt:   stopAt,
			fetch: func(ctx context.Context, page int) ([]hardstyle.Track, error) {
				tracks, err := hardstyle.FetchPage(ctx, hc, genre, page)
				if errors.Is(err, hardstyle.ErrNoResults) {
					return nil, errEndOfList
				}
				return tracks, err
			},
		})
	}
	for _, genre := range cfg.HardtunesGenres {
		name := "hardtunes.com " + hardtunes.GenreLabel(genre)
		feeds = append(feeds, &feed{
			name:     name,
			genre:    hardtunes.GenreLabel(genre),
			delay:    hardtunes.PageDelay,
			maxPages: cfg.Pages,
			stopAt:   stopAt,
			fetch: func(ctx context.Context, page int) ([]hardstyle.Track, error) {
				tracks, err := hardtunes.FetchPage(ctx, hc, genre, page)
				if errors.Is(err, hardtunes.ErrNoResults) {
					return nil, errEndOfList
				}
				if err != nil {
					return nil, err
				}
				// An album is a bundle, not a track: its tunes take its place.
				if tracks, err = hardtunes.ExpandAlbums(ctx, hc, tracks); err != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					w.warn(emit, i18n.T("%s: Albumseite nicht lesbar, ihre Tracks fehlen in diesem Lauf: %v", "%s: album page not readable, its tracks are left out this run: %v"), name, err)
				}
				out := make([]hardstyle.Track, len(tracks))
				for i, t := range tracks {
					rel := t.Release()
					// The list names fresh releases by day ("Today") and older ones only by week ("Last week").
					// The day seen once stays: the service looks every few minutes, so it sees most releases on
					// their day.
					if e, ok := c.Entry(rel); ok && e.DateExact && !t.DateExact {
						t.Date, t.DateExact = e.Date, true
					} else if t.DateExact && (!ok || !e.DateExact) {
						c.RememberDate(rel, t.Date)
					}
					out[i] = rel
					w.hardtunes[t.Path] = t
				}
				return out, nil
			},
		})
	}

	// The date of a release: hardtunes.com names it in its list, hardstyle.com on the release page, which the
	// cache keeps. A page that cannot be read leaves the date unknown; the release then counts as new as the one
	// before it in its list.
	details := hardstyle.NewDetailFetcher(hc, hardstyle.DetailDelay)
	undated := map[string]bool{}
	dateOf := func(ctx context.Context, t hardstyle.Track) string {
		if ht, ok := w.hardtunes[t.Path]; ok {
			if ht.Date == "" {
				undated[t.Path] = true
			}
			return ht.Date
		}
		if e, ok := c.Entry(t); ok && !e.DetailsDue() {
			return e.Date
		}
		d, err := details.Fetch(ctx, t)
		if err != nil || d.Date == "" {
			undated[t.Path] = true
			return ""
		}
		c.SetDetails(t, d)
		w.detailsRead[t.Path] = true
		return d.Date
	}
	emit.Send(event.Event{Kind: event.EvPhase, Phase: event.PhaseFetch, Done: 0, Total: cfg.MaxTracks})
	tracks, err := newestReleases(ctx, feeds, windowOptions{
		limit:       cfg.MaxTracks,
		minPerGenre: cfg.GenreMin,
		dateOf:      dateOf,
		isrcOf: func(t hardstyle.Track) string {
			e, _ := c.Entry(t)
			return e.ISRC
		},
		progress: func(taken int) {
			emit.Send(event.Event{Kind: event.EvPhase, Phase: event.PhaseFetch, Done: taken, Total: cfg.MaxTracks})
		},
	})
	if saveErr := c.Save(); saveErr != nil {
		emit.Logf(i18n.T("Zwischenspeicher nicht gespeichert: %v", "Cache not saved: %v"), saveErr)
	}
	if err != nil {
		return w, fmt.Errorf(i18n.T("keine Liste lesbar: %w", "no list readable: %w"), err)
	}
	emit.Send(event.Event{Kind: event.EvPhase, Phase: event.PhaseFetch, Done: cfg.MaxTracks, Total: cfg.MaxTracks})
	pages := 0
	for _, f := range feeds {
		pages += f.page
		if f.page > 0 {
			emit.Logf(i18n.T("%s: %d Seiten gelesen, %d Releases im Fenster", "%s: %d pages read, %d releases in the window"), f.name, f.page, f.taken)
		}
	}
	emit.Logf(i18n.T("Die neuesten %d Releases (Limit %d) aus %d Listen, %d Seiten gelesen", "The newest %d releases (limit %d) from %d lists, %d pages read"), len(tracks), cfg.MaxTracks, len(feeds), pages)
	w.tracks = tracks
	w.checkHealth(feeds, undated, emit)
	return w, nil
}

// warn notes something that looked wrong: in the log now, and in the summary of the run.
func (w *window) warn(emit event.Emit, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	w.warnings = append(w.warnings, msg)
	emit.Logf("%s", i18n.T("Warnung: ", "Warning: ")+msg)
}

// minForCoverage is the number of releases from which a share that is too small means trouble, not chance.
const minForCoverage = 20

// checkHealth looks at what the lists gave and warns when it looks like a site changed: a list that could not
// be read, a list that was empty from the first page, many releases without a date, or hardstyle.com lists
// without the lengths of the tracks.
func (w *window) checkHealth(feeds []*feed, undated map[string]bool, emit event.Emit) {
	for _, f := range feeds {
		switch {
		case f.err != nil:
			w.warn(emit, i18n.T("%s nicht lesbar, in diesem Lauf ausgelassen: %v", "%s not readable, left out of this run: %v"), f.name, f.err)
		case f.empty:
			w.warn(emit, i18n.T("%s ist leer – gibt es das Genre nicht mehr, oder hat sich die Seite geändert?", "%s is empty – is the genre gone, or did the site change?"), f.name)
		}
	}
	if n := len(w.tracks); n >= minForCoverage {
		missing := 0
		for _, t := range w.tracks {
			if undated[t.Path] {
				missing++
			}
		}
		if missing*10 > n {
			w.warn(emit, i18n.T("%d von %d Releases ohne Datum – hat sich eine Seite geändert?", "%d of %d releases without a date – did a site change?"), missing, n)
		}
	}
	hs, timed := 0, 0
	for _, t := range w.tracks {
		if !fromHardtunes(t) {
			hs++
			if t.Duration > 0 {
				timed++
			}
		}
	}
	if hs >= minForCoverage && timed == 0 {
		w.warn(emit, "%s", i18n.T("keine Tracklängen in den Listen von hardstyle.com – hat sich die Seite geändert?", "no track lengths in the hardstyle.com lists – did the site change?"))
	}
}

// logEmit prints the log events, for runs without the interface.
func logEmit(ev event.Event) {
	if ev.Kind == event.EvLog {
		log.Print(ev.Msg)
	}
}

// applyCorrections reads the corrections file and applies it to the cache: a retry drops the remembered track,
// a skip refuses one Spotify track for good. Both make the next lookup of that release happen again. A line that
// cannot be read is reported and skipped. The cache is saved at once, so a cancelled run does not lose it.
func applyCorrections(path string, c *cache.Cache, emit event.Emit) {
	if path == "" {
		return
	}
	list, err := corrections.Parse(path)
	if err != nil {
		emit.Logf(i18n.T("Korrekturen nicht vollständig gelesen: %v", "Corrections not fully read: %v"), err)
	}
	if len(list) == 0 {
		return
	}
	for _, corr := range list {
		t := hardstyle.Track{ID: corr.ID}
		switch corr.Action {
		case corrections.Retry:
			c.Forget(t)
		case corrections.Skip:
			c.Reject(t, corr.URI)
		case corrections.Force:
			c.Force(t, corr.URI)
		}
	}
	if err := c.Save(); err != nil {
		emit.Logf(i18n.T("Korrekturen nicht gespeichert: %v", "Corrections not saved: %v"), err)
	}
}

// countWork counts the releases that need a lookup on Spotify: those never looked up, and the missing ones that
// are due again.
func countWork(releases []hardstyle.Track, c *cache.Cache) (fresh, due int) {
	for _, t := range releases {
		switch e, ok := c.Entry(t); {
		case !ok || e.At.IsZero():
			fresh++
		case !e.Usable():
			due++
		}
	}
	return fresh, due
}

// showFromCache shows the releases of a check that found nothing new, with what the cache knows about them,
// so the screen is not empty.
func showFromCache(releases []hardstyle.Track, playlistID string, c *cache.Cache, emit event.Emit) {
	emit.Send(event.Event{Kind: event.EvReleases, Releases: releases})
	var inPlaylist map[string]bool
	if p := c.Playlist(playlistID); p != nil {
		inPlaylist = setOf(p.URIs)
		emit.Send(event.Event{Kind: event.EvPlaylist, Playlist: event.StatsFor(p.URIs, c.DatesByURI())})
	}
	for i, t := range releases {
		e, ok := c.Entry(t)
		r := resolvedFrom(e, ok)
		emit.Send(event.Event{Kind: event.EvItem, Index: i, Status: statusOf(r, inPlaylist), URI: r.primary(), Date: r.date, Note: r.note()})
	}
}

// schedule decides when the next run is due.
type Schedule struct {
	// Every is the pause between two complete runs; 0 means no regular full runs.
	Every time.Duration
	// Watch is the pause between two checks for new tracks (quick runs); 0 means no checks.
	Watch time.Duration
	// Continue is the pause before the next run when work is left over or a run failed.
	Continue time.Duration
	// DryRun runs search a lot but write nothing, so they do not carry on by themselves.
	DryRun bool
}

// next returns when the next run is due, why, and whether it is a quick one. A duration of 0 means: no next run.
// sinceFull is the time since the last full run.
//
//   - A run that stopped at the search limit goes on after a short pause, until every release is done.
//   - After a rate limit it goes on when Spotify lets the app in again.
//   - After another error it tries again, with growing pauses (failures counts the errors in a row).
//   - A complete run is followed by the next check for new tracks, or by the regular full run when that is due.
func (s Schedule) Next(res Result, err error, failures int, sinceFull time.Duration) (time.Duration, string, bool) {
	var limit *spotify.RateLimitError
	switch {
	case errors.Is(err, context.Canceled):
		return 0, "", false
	case errors.As(err, &limit):
		return max(time.Until(limit.Until), 0) + time.Minute, i18n.T("Spotify-Sperre", "Spotify block"), false
	case err != nil:
		d := s.Continue
		for i := 1; i < failures && d < time.Hour; i++ {
			d *= 2
		}
		if d == 0 {
			return 0, "", false
		}
		return min(d, time.Hour), i18n.T("nach einem Fehler", "after an error"), false
	case res.Deferred > 0 && !s.DryRun && s.Continue > 0:
		return s.Continue, fmt.Sprintf(i18n.T("%d warten", "%d waiting"), res.Deferred), false
	case s.Watch > 0:
		if s.Every > 0 && sinceFull+s.Watch >= s.Every {
			return max(s.Every-sinceFull, 0), i18n.T("geplanter Lauf", "scheduled run"), false
		}
		return s.Watch, i18n.T("neue Tracks", "new tracks"), true
	}
	return s.Every, i18n.T("geplanter Lauf", "scheduled run"), false
}

// maxDescription is the longest description Spotify takes.
const maxDescription = 300

// descriptionOff in PLAYLIST_DESCRIPTION leaves the description of the playlist alone.
const descriptionOff = "off"

// defaultDescription is the description of the playlist, in English. {source}, {sites}, {order}, {max} and
// {date} are filled in.
const defaultDescription = "Daily updated. The latest {source} releases from {sites}, {order}, max. {max} tracks. " +
	"Last update: {date}"

// orderText says the order of the playlist in the description.
var orderText = map[config.Order]string{
	config.OrderNewest: "newest first",
	config.OrderOldest: "oldest first",
	config.OrderArtist: "sorted by artist",
	config.OrderTitle:  "sorted by title",
}

// playlistDescription builds the description of the playlist: what it is, where it comes from and when it was
// updated last. PLAYLIST_DESCRIPTION replaces the text; {source}, {sites}, {order}, {max} and {date} are filled in.
func playlistDescription(cfg config.Config, now time.Time) string {
	text := cfg.Description
	if text == "" {
		text = defaultDescription
	}
	source := "hardstyle"
	switch {
	case len(cfg.Genres) == 1 && cfg.Genres[0] == hardstyle.AnyGenre:
		source = "all-genre"
	case len(cfg.Genres) > 0:
		source = strings.Join(cfg.Genres, ", ")
	}
	sites := "hardstyle.com"
	if len(cfg.HardtunesGenres) > 0 {
		sites = "hardstyle.com and hardtunes.com"
	}
	text = strings.NewReplacer(
		"{source}", source,
		"{sites}", sites,
		"{order}", orderText[cmp.Or(cfg.Order, config.OrderNewest)],
		"{max}", fmt.Sprint(cfg.MaxTracks),
		"{date}", now.Format("2 Jan 2006"),
	).Replace(text)
	if r := []rune(text); len(r) > maxDescription {
		text = string(r[:maxDescription-1]) + "…"
	}
	return text
}

// updateDescription writes the description of the playlist. Only when the text changed, which is once a day at
// most, because it has the date in it. A failure is no reason to fail the run.
func updateDescription(ctx context.Context, sp *spotify.Client, cfg config.Config, c *cache.Cache, now time.Time, emit event.Emit) {
	if strings.EqualFold(cfg.Description, descriptionOff) {
		return
	}
	text := playlistDescription(cfg, now)
	if c.Description(cfg.PlaylistID) == text {
		return
	}
	if err := sp.SetDescription(ctx, cfg.PlaylistID, text); err != nil {
		emit.Logf(i18n.T("Beschreibung der Playlist nicht geändert: %v", "Playlist description not changed: %v"), err)
		return
	}
	c.SetDescription(cfg.PlaylistID, text)
	if err := c.Save(); err != nil {
		emit.Logf(i18n.T("Zwischenspeicher nicht gespeichert: %v", "Cache not saved: %v"), err)
	}
	emit.Logf(i18n.T("Beschreibung der Playlist aktualisiert: %s", "Playlist description updated: %s"), text)
}
