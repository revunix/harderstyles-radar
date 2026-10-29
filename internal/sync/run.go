package sync

import (
	"context"
	"errors"
	"fmt"
	"hardstylesync/internal/cache"
	"hardstylesync/internal/config"
	"hardstylesync/internal/event"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/notify"
	"hardstylesync/internal/spotify"
	"log"
	"net/http"
	"strings"
	"time"
)

// Run does one run and sends its summary to the Discord webhook when one is set.
func Run(ctx context.Context, hc *http.Client, sp *spotify.Client, cfg config.Config, dryRun, quick bool, emit event.Emit) (Result, error) {
	res, err := run(ctx, hc, sp, cfg, dryRun, quick, emit)
	notifyRun(ctx, hc, cfg, res, err, dryRun, quick, sp, emit)
	return res, err
}

// notifyRun sends the summary to Discord. A check for new tracks that found nothing stays quiet, a full run always
// reports, a failed run reports its error. A failed message is only logged: the run itself was fine.
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
	case quick && res.Added == 0 && res.Removed == 0:
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
		})
	}
	if nerr != nil {
		emit.Logf(i18n.T("Discord-Nachricht nicht gesendet: %v", "Discord message not sent: %v"), nerr)
	}
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

	var perGenre [][]hardstyle.Track
	for i, genre := range cfg.Genres {
		emit.Send(event.Event{Kind: event.EvPhase, Phase: event.PhaseFetch, Done: i, Total: len(cfg.Genres), Msg: genre})
		var tracks []hardstyle.Track
		var err error
		if quick {
			tracks, err = hardstyle.FetchUntilKnown(ctx, hc, genre, cfg.Pages, func(t hardstyle.Track) bool {
				e, ok := c.Entry(t)
				return ok && !e.At.IsZero()
			})
		} else {
			tracks, err = hardstyle.FetchReleases(ctx, hc, genre, cfg.Pages)
		}
		if err != nil {
			return Result{}, fmt.Errorf(i18n.T("%s lesen: %w", "reading %s: %w"), genre, err)
		}
		emit.Logf(i18n.T("hardstyle.com %s: %d Releases", "hardstyle.com %s: %d releases"), hardstyle.GenreLabel(genre), len(tracks))
		perGenre = append(perGenre, tracks)
	}
	emit.Send(event.Event{Kind: event.EvPhase, Phase: event.PhaseFetch, Done: len(cfg.Genres), Total: len(cfg.Genres)})

	releases := interleave(perGenre)
	if len(releases) == 0 {
		return Result{}, errors.New(i18n.T("keine Releases auf hardstyle.com gefunden", "no releases found on hardstyle.com"))
	}

	if quick {
		fresh, due := countWork(releases, c)
		if fresh+due == 0 {
			emit.Logf(i18n.T("Keine neuen Tracks auf hardstyle.com (%d geprüft), nichts zu tun", "No new tracks on hardstyle.com (%d checked), nothing to do"), len(releases))
			showFromCache(releases, cfg.PlaylistID, c, emit)
			emit.Send(event.Event{Kind: event.EvPhase, Phase: event.PhaseDone, Done: 1, Total: 1})
			return Result{Scraped: len(releases)}, nil
		}
		emit.Logf(i18n.T("%d neue und %d erneut zu prüfende Tracks gefunden", "%d new and %d to check again found"), fresh, due)
	}

	res, err := Sync(ctx, sp, cfg.PlaylistID, releases, Options{
		MaxTracks:      cfg.MaxTracks,
		MaxSearches:    cfg.MaxSearches,
		DateTolerance:  cfg.DateTolerance,
		AlbumMinTracks: cfg.AlbumMinTracks,
		DryRun:         dryRun,
		KeepSiteOrder:  len(cfg.Genres) == 1,
		FetchDetails:   hardstyle.NewDetailFetcher(hc, hardstyle.DetailDelay).Fetch,
	}, c, emit)
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

// interleave takes one track from each genre in turn, so a MaxTracks cap treats all genres alike.
func interleave(lists [][]hardstyle.Track) []hardstyle.Track {
	var out []hardstyle.Track
	for i := 0; ; i++ {
		added := false
		for _, l := range lists {
			if i < len(l) {
				out = append(out, l[i])
				added = true
			}
		}
		if !added {
			return out
		}
	}
}

// logEmit prints the log events, for runs without the interface.
func logEmit(ev event.Event) {
	if ev.Kind == event.EvLog {
		log.Print(ev.Msg)
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

// defaultDescription is the description of the playlist, in English. {source}, {max} and {date} are filled in.
const defaultDescription = "Daily updated. The latest {source} releases from hardstyle.com, in the same order as on the site " +
	"(newest first), max. {max} tracks. Last update: {date}"

// playlistDescription builds the description of the playlist: what it is, where it comes from and when it was
// updated last. PLAYLIST_DESCRIPTION replaces the text; {source}, {max} and {date} are filled in.
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
	text = strings.NewReplacer(
		"{source}", source,
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
