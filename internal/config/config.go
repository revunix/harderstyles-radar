// Package config reads the settings of the tool from the environment and a .env file.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/hardtunes"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/spotify"
)

type Config struct {
	ClientID, ClientSecret, RefreshToken, PlaylistID string
	// Credentials are the Spotify apps in the order they are used: the first one, then the fallbacks
	// SPOTIFY_CLIENT_ID_2 ... that take over when an app is blocked.
	Credentials []spotify.Credential
	Genres      []string
	// HardtunesGenres are the genre lists of hardtunes.com, the second source (HARDTUNES_GENRES); none = off.
	HardtunesGenres                            []string
	CacheFile                                  string
	CorrectionsFile                            string // releases whose Spotify track was refused; empty derives it from the cache file
	Pages, MaxTracks                           int
	MaxSearches, DateTolerance, AlbumMinTracks int
	RequestDelay, ContinueDelay, WatchInterval time.Duration
	Description                                string
	// Order is how the playlist is sorted (PLAYLIST_ORDER). The newest MaxTracks releases are kept whatever the
	// order; it only decides where they stand.
	Order Order
	// DiscordWebhook sends the summary of a run to Discord; empty switches it off.
	DiscordWebhook string
	// GenreMin releases of every genre are kept in the window, however busy the other genres are (GENRE_MIN);
	// 0 = only the release date counts.
	GenreMin int
	// EvalSamples is how many releases per run that the ISRC finds are also searched by title, so the
	// evaluation (-evaluate) can check the title matching against the ISRC's answer (EVAL_SAMPLES). Each costs
	// one or two extra Spotify searches; 0 switches it off.
	EvalSamples int
}

// Order is how the playlist is sorted.
type Order string

const (
	OrderNewest Order = "newest" // newest release date first, the default
	OrderOldest Order = "oldest" // oldest release date first, the newest at the end
	OrderArtist Order = "artist" // by artist, then title
	OrderTitle  Order = "title"  // by title, then artist
)

// Orders are the values PLAYLIST_ORDER takes.
var Orders = []Order{OrderNewest, OrderOldest, OrderArtist, OrderTitle}

// allGenres are the genre filters offered by hardstyle.com/en/music.
var allGenres = []string{"Hardstyle", "Hardcore", "Uptempo", "Frenchcore", "Freestyle", "Hard Dance", "Hard Techno", "UK/Happy Hardcore"}

// Load reads the settings; needRefresh also demands the refresh token and the playlist.
func Load(needRefresh bool) (Config, error) { return load(needRefresh, true) }

// LoadCompare reads only the source settings (genres and pages) for -compare; no Spotify credentials needed.
func LoadCompare() (Config, error) { return load(false, false) }

func load(needRefresh, needSpotify bool) (Config, error) {
	c := Config{
		ClientID:        os.Getenv("SPOTIFY_CLIENT_ID"),
		ClientSecret:    os.Getenv("SPOTIFY_CLIENT_SECRET"),
		RefreshToken:    os.Getenv("SPOTIFY_REFRESH_TOKEN"),
		PlaylistID:      os.Getenv("SPOTIFY_PLAYLIST_ID"),
		Pages:           envInt("PAGES", 0), // 0: decided below, it depends on the genres
		MaxTracks:       envInt("MAX_TRACKS", 500),
		CacheFile:       envStr("CACHE_FILE", ".hardstyle-sync-cache.json"),
		CorrectionsFile: envStr("CORRECTIONS_FILE", ""),
		Description:     os.Getenv("PLAYLIST_DESCRIPTION"),
		// Spotify limits the requests in a time window and may block an app for a day, so everything is slow
		// and small by default.
		MaxSearches:   envInt("MAX_SEARCHES", 100),
		GenreMin:      envInt("GENRE_MIN", 0),
		EvalSamples:   envInt("EVAL_SAMPLES", 5),
		DateTolerance: envInt("DATE_TOLERANCE_DAYS", 14), // a couple of days is normal, more is a wrong version
		// This many releases with the same catalog number on hardstyle.com make an album, whose tracks all come
		// from the same Spotify album (0 switches it off).
		AlbumMinTracks: envInt("ALBUM_MIN_TRACKS", 2),
		// The service checks hardstyle.com for new tracks this often; 0 switches the checks off.
		WatchInterval: envDuration("WATCH_INTERVAL", 0),
		RequestDelay:  envDuration("REQUEST_DELAY", 800*time.Millisecond),
		// After a run that stopped at the search limit (or failed) the next one follows after this pause.
		ContinueDelay:  envDuration("CONTINUE_DELAY", 10*time.Minute),
		DiscordWebhook: strings.TrimSpace(os.Getenv("DISCORD_WEBHOOK_URL")),
	}
	for _, g := range strings.Split(envStr("GENRES", "Hardstyle"), ",") {
		switch g = strings.TrimSpace(g); {
		case strings.EqualFold(g, "any") || g == "*":
			// The general list, whatever the genre. It makes no sense to mix it with single genres.
			c.Genres = []string{hardstyle.AnyGenre}
			g = ""
		case strings.EqualFold(g, "all"):
			c.Genres = append(c.Genres, allGenres...)
			g = ""
		}
		if g != "" {
			c.Genres = append(c.Genres, g)
		}
		if len(c.Genres) == 1 && c.Genres[0] == hardstyle.AnyGenre {
			break
		}
	}
	for _, g := range strings.Split(envStr("HARDTUNES_GENRES", strings.Join(hardtunes.Genres, ",")), ",") {
		switch g = strings.TrimSpace(g); {
		case strings.EqualFold(g, "none") || strings.EqualFold(g, "off"):
			c.HardtunesGenres = nil
		case strings.EqualFold(g, "all") || g == "*":
			c.HardtunesGenres = append(c.HardtunesGenres, hardtunes.Genres...)
		case g != "":
			c.HardtunesGenres = append(c.HardtunesGenres, g)
		}
	}
	if c.Pages == 0 {
		// A run reads each list only as far as the newest MAX_TRACKS releases of all lists need, so PAGES is only
		// the most it may read of one list: enough for MAX_TRACKS from that list alone (hardtunes.com shows 20 per
		// page). -compare reads every page it may, so it keeps to a short look.
		c.Pages = (c.MaxTracks + 19) / 20
		if !needSpotify {
			c.Pages = 2
		}
	}
	c.Order = Order(strings.ToLower(strings.TrimSpace(envStr("PLAYLIST_ORDER", string(OrderNewest)))))
	if !slices.Contains(Orders, c.Order) {
		return c, fmt.Errorf(i18n.T("PLAYLIST_ORDER %q unbekannt, erlaubt: newest, oldest, artist, title", "PLAYLIST_ORDER %q unknown, use newest, oldest, artist or title"), c.Order)
	}
	if c.GenreMin < 0 || c.EvalSamples < 0 {
		return c, errors.New(i18n.T("GENRE_MIN und EVAL_SAMPLES dürfen nicht negativ sein", "GENRE_MIN and EVAL_SAMPLES must not be negative"))
	}
	var missing []string
	if needSpotify {
		for name, v := range map[string]string{"SPOTIFY_CLIENT_ID": c.ClientID, "SPOTIFY_CLIENT_SECRET": c.ClientSecret} {
			if v == "" {
				missing = append(missing, name)
			}
		}
	}
	if needRefresh {
		if c.RefreshToken == "" {
			missing = append(missing, i18n.T("SPOTIFY_REFRESH_TOKEN (mit -auth holen)", "SPOTIFY_REFRESH_TOKEN (run with -auth)"))
		}
		if c.PlaylistID == "" {
			missing = append(missing, "SPOTIFY_PLAYLIST_ID")
		}
	}
	c.Credentials = []spotify.Credential{{ClientID: c.ClientID, ClientSecret: c.ClientSecret, RefreshToken: c.RefreshToken}}
	for n := 2; ; n++ {
		sfx := "_" + strconv.Itoa(n)
		cr := spotify.Credential{ClientID: os.Getenv("SPOTIFY_CLIENT_ID" + sfx), ClientSecret: os.Getenv("SPOTIFY_CLIENT_SECRET" + sfx), RefreshToken: os.Getenv("SPOTIFY_REFRESH_TOKEN" + sfx)}
		if cr == (spotify.Credential{}) {
			break
		}
		for name, v := range map[string]string{"SPOTIFY_CLIENT_ID": cr.ClientID, "SPOTIFY_CLIENT_SECRET": cr.ClientSecret, "SPOTIFY_REFRESH_TOKEN": cr.RefreshToken} {
			if v == "" && (needRefresh || name != "SPOTIFY_REFRESH_TOKEN") {
				missing = append(missing, name+sfx)
			}
		}
		c.Credentials = append(c.Credentials, cr)
	}
	if len(missing) > 0 {
		return c, fmt.Errorf(i18n.T("fehlende Einstellungen: %s", "missing environment: %s"), strings.Join(missing, ", "))
	}
	if c.MaxTracks < 1 || c.Pages < 1 {
		return c, errors.New(i18n.T("MAX_TRACKS und PAGES müssen mindestens 1 sein", "MAX_TRACKS and PAGES must be at least 1"))
	}
	if c.CorrectionsFile == "" {
		c.CorrectionsFile = filepath.Join(filepath.Dir(c.CacheFile), "corrections.txt")
	}
	if c.DiscordWebhook != "" {
		u, err := url.Parse(c.DiscordWebhook)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			// The address carries the token of the webhook, so it is not repeated here.
			return c, errors.New(i18n.T("DISCORD_WEBHOOK_URL ist keine gültige http(s)-Adresse", "DISCORD_WEBHOOK_URL is not a valid http(s) URL"))
		}
	}
	return c, nil
}

func envStr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(k)); err == nil && d >= 0 {
		return d
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}
