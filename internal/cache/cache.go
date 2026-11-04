package cache

import (
	"encoding/json"
	"errors"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/match"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

const (
	cacheVersion = 2
	// matcherVersion counts the changes of how releases are matched to Spotify tracks. When it grows, the
	// releases that were not found are looked for again at once: the better matching may find them now.
	matcherVersion = 6
	// detailsVersion counts what is read from the page of a release. When it grows, the pages are read again
	// once (2: the catalog number).
	detailsVersion = 2
	// missingRecheck is how long a "missing" answer holds. Then the next run looks again, because Spotify
	// often gets a release a few hours or days after hardstyle.com.
	missingRecheck = 12 * time.Hour
	// cacheMaxAge drops entries nobody asked for in a long time.
	cacheMaxAge = 120 * 24 * time.Hour
	// PlaylistVerifyEvery forces a full read of the playlist now and then, even if the snapshot is unchanged.
	PlaylistVerifyEvery = 7 * 24 * time.Hour
)

// Entry is what is known about one release of hardstyle.com.
type Entry struct {
	URI         string    `json:"uri"`                    // "" means: not found on Spotify
	Extra       []string  `json:"extra,omitempty"`        // more versions (remixes) of the song that go into the playlist too
	ISRC        string    `json:"isrc,omitempty"`         // code of the recording, from the page on hardstyle.com
	Album       bool      `json:"album,omitempty"`        // taken from the Spotify album of its catalog number
	AlbumID     string    `json:"album_id,omitempty"`     // the Spotify album of the match
	Catalog     string    `json:"catalog,omitempty"`      // catalog number on hardstyle.com, the same for all tracks of an album
	DetailsV    int       `json:"details_v,omitempty"`    // detailsVersion of the page read last
	DaysOff     int       `json:"days_off,omitempty"`     // the title and artist fit, but the date is this many days off
	At          time.Time `json:"at"`                     // when it was searched last
	Date        string    `json:"date,omitempty"`         // release date on hardstyle.com, yyyy-mm-dd
	SpotifyDate string    `json:"spotify_date,omitempty"` // release date of the match on Spotify
	Reason      string    `json:"reason,omitempty"`       // why it is missing: spotify.ReasonNotFound or spotify.ReasonDate
	Tries       int       `json:"tries,omitempty"`        // how often it was searched without success
	Rejected    []string  `json:"rejected,omitempty"`     // Spotify tracks a correction refused; they are never chosen again
	Forced      string    `json:"forced,omitempty"`       // a Spotify track a correction chose by hand; it wins over the search
	// ByISRC: the match came from the ISRC, as sure as a match gets. The evaluation takes it as the truth.
	ByISRC bool `json:"by_isrc,omitempty"`
	// DateExact: Date is the real day, not an estimate (hardtunes.com names some releases only by week).
	DateExact bool `json:"date_exact,omitempty"`
	// Release is the release as the site listed it when it was looked up last, and Searched what the title
	// searches of Spotify found then: the evaluation replays the matching on them without a request.
	Release  *StoredRelease `json:"release,omitempty"`
	Searched []StoredTrack  `json:"searched,omitempty"`
}

// StoredRelease is a release as the site listed it, kept for the evaluation.
type StoredRelease struct {
	Title    string `json:"title"`
	Version  string `json:"version,omitempty"`
	Artist   string `json:"artist"`
	Label    string `json:"label,omitempty"`
	Duration int    `json:"duration,omitempty"` // seconds
	Date     string `json:"date,omitempty"`
	ISRC     string `json:"isrc,omitempty"`
}

// StoredTrack is a Spotify track a title search found, kept for the evaluation.
type StoredTrack struct {
	URI           string   `json:"uri"`
	Name          string   `json:"name"`
	Artists       []string `json:"artists"`
	ArtistIDs     []string `json:"artist_ids,omitempty"`
	ReleaseDate   string   `json:"release_date,omitempty"`
	DatePrecision string   `json:"date_precision,omitempty"`
	Duration      int      `json:"duration,omitempty"` // seconds
	AlbumID       string   `json:"album_id,omitempty"`
	AlbumName     string   `json:"album_name,omitempty"`
	AlbumType     string   `json:"album_type,omitempty"`
	AlbumTracks   int      `json:"album_tracks,omitempty"`
}

// DetailsDue tells whether the page of the release on hardstyle.com has to be read (again): it was never read,
// or before the tool read everything it reads now.
func (e Entry) DetailsDue() bool {
	return e.Date == "" || e.DetailsV < detailsVersion
}

// usable tells whether the answer needs no new search: a found track always, a missing one until it is due.
func (e Entry) Usable() bool {
	return e.URI != "" || (!e.At.IsZero() && time.Since(e.At) < missingRecheck)
}

// PlaylistSnapshot is what the cache knows about the playlist: the tracks in their order and the snapshot id
// Spotify gave for exactly that state.
type PlaylistSnapshot struct {
	ID         string    `json:"id"`
	SnapshotID string    `json:"snapshot_id"`
	URIs       []string  `json:"uris"`
	At         time.Time `json:"at"`
}

// describedPlaylist is the description the tool wrote last, so the same text is not written twice.
type describedPlaylist struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// AlbumTrack is a track of a Spotify album.
type AlbumTrack struct {
	URI     string   `json:"uri"`
	Name    string   `json:"name"`
	Artists []string `json:"artists"`
}

// AlbumEntry is what is known about a Spotify album: its tracks never change, so they are read only once.
type AlbumEntry struct {
	Name   string       `json:"name"`
	Date   string       `json:"date,omitempty"`
	Tracks []AlbumTrack `json:"tracks"`
	At     time.Time    `json:"at"`
}

type cacheFile struct {
	Version      int                   `json:"version"`
	Matcher      int                   `json:"matcher,omitempty"`
	Tracks       map[string]Entry      `json:"tracks"`
	Albums       map[string]AlbumEntry `json:"albums,omitempty"`
	Playlist     *PlaylistSnapshot     `json:"playlist,omitempty"`
	Description  *describedPlaylist    `json:"description,omitempty"`
	BlockedUntil time.Time             `json:"blocked_until,omitempty"` // before several apps: the block of the only one
	Blocked      map[string]time.Time  `json:"blocked,omitempty"`       // by client id
	// Aliases maps a site artist name (match.NormName) to the Spotify artist ids it was confirmed as (by ISRC).
	Aliases map[string][]string `json:"aliases,omitempty"`
}

// Cache keeps what the tool learned, so a daily run asks Spotify as little as possible: which release is which
// Spotify track, which ones are missing, the release dates, the playlist, and when Spotify blocks the app.
// A nil Cache does nothing.
type Cache struct {
	mu    sync.Mutex
	path  string
	f     cacheFile
	dirty bool
}

// Load reads the cache file. A missing or unreadable file is an empty cache.
func Load(path string) (*Cache, error) {
	c := &Cache{path: path, f: cacheFile{Version: cacheVersion, Tracks: map[string]Entry{}}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	var loaded cacheFile
	if json.Unmarshal(data, &loaded) == nil && loaded.Version == cacheVersion {
		if loaded.Tracks == nil {
			loaded.Tracks = map[string]Entry{}
		}
		c.f = loaded
	}
	if c.f.Matcher < matcherVersion {
		for k, e := range c.f.Tracks {
			if e.URI == "" && !e.At.IsZero() {
				e.At = time.Time{} // not searched yet, as far as the new matching is concerned
				c.f.Tracks[k] = e
			}
		}
		c.f.Matcher, c.dirty = matcherVersion, true
	}
	return c, nil
}

func cacheKey(t hardstyle.Track) string {
	if t.ID != "" {
		return t.ID
	}
	return match.NormName(t.Title + "|" + t.Artist)
}

// Entry returns what is known about a release.
func (c *Cache) Entry(t hardstyle.Track) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.f.Tracks[cacheKey(t)]
	return e, ok
}

func (c *Cache) update(t hardstyle.Track, change func(*Entry)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.f.Tracks[cacheKey(t)]
	change(&e)
	c.f.Tracks[cacheKey(t)] = e
	c.dirty = true
}

// SetDetails remembers the release date, the ISRC, the catalog number and the length from the release page.
// They never change, so they are asked for only once. An estimated date (DateApprox) does not replace a day
// that is known exactly.
func (c *Cache) SetDetails(t hardstyle.Track, d hardstyle.Details) {
	c.update(t, func(e *Entry) {
		e.DetailsV = detailsVersion
		if !d.DateApprox || !e.DateExact || e.Date == "" {
			e.Date, e.DateExact = d.Date, !d.DateApprox && d.Date != ""
		}
		if d.ISRC != "" {
			e.ISRC = d.ISRC
		}
		if d.Catalog != "" {
			e.Catalog = d.Catalog
		}
	})
}

// RememberDate keeps a release day the list named exactly ("Today" on hardtunes.com). Later the list may name
// only the week; the day stays. The release page still counts as unread.
func (c *Cache) RememberDate(t hardstyle.Track, date string) {
	if date == "" {
		return
	}
	c.update(t, func(e *Entry) {
		e.Date, e.DateExact = date, true
	})
}

// SetSearched keeps the release as listed and what the title searches found, for the evaluation.
func (c *Cache) SetSearched(t hardstyle.Track, r StoredRelease, searched []StoredTrack) {
	c.update(t, func(e *Entry) {
		e.Release, e.Searched = &r, searched
	})
}

// Each calls fn for every release the cache knows, in no particular order.
func (c *Cache) Each(fn func(e Entry)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	entries := make([]Entry, 0, len(c.f.Tracks))
	for _, e := range c.f.Tracks {
		entries = append(entries, e)
	}
	c.mu.Unlock()
	for _, e := range entries {
		fn(e)
	}
}

// ArtistIDs returns the Spotify artist ids a site artist name (match.NormName) was confirmed as.
func (c *Cache) ArtistIDs(name string) []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.f.Aliases[name]
}

// LearnArtist remembers that a site artist name (match.NormName) is the Spotify artist id.
func (c *Cache) LearnArtist(name, id string) {
	if c == nil || name == "" || id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if slices.Contains(c.f.Aliases[name], id) {
		return
	}
	if c.f.Aliases == nil {
		c.f.Aliases = map[string][]string{}
	}
	c.f.Aliases[name], c.dirty = append(c.f.Aliases[name], id), true
}

// Found is the Spotify answer for a release.
type Found struct {
	URIs        []string // the best match first, then more versions of the song
	SpotifyDate string
	Album       bool   // taken from the Spotify album of its catalog number
	AlbumID     string // the Spotify album of the best match
	DaysOff     int    // the date of the match is this many days off
	ByISRC      bool   // the match came from the ISRC
}

// SetFound remembers the Spotify tracks of a release. A track a correction refused is left out; if that was
// the only one, the release counts as not found.
func (c *Cache) SetFound(t hardstyle.Track, f Found) {
	c.update(t, func(e *Entry) {
		uris := f.URIs
		if len(e.Rejected) > 0 {
			drop := map[string]bool{}
			for _, u := range e.Rejected {
				drop[u] = true
			}
			kept := uris[:0:0]
			for _, u := range uris {
				if !drop[u] {
					kept = append(kept, u)
				}
			}
			uris = kept
		}
		if len(uris) == 0 {
			e.URI, e.Extra, e.ByISRC = "", nil, false
			e.At, e.Reason = time.Now(), "notfound"
			return
		}
		e.URI, e.Extra = uris[0], append([]string(nil), uris[1:]...)
		e.At, e.SpotifyDate, e.Reason, e.Tries = time.Now(), f.SpotifyDate, "", 0
		e.Album, e.AlbumID, e.DaysOff, e.ByISRC = f.Album, f.AlbumID, f.DaysOff, f.ByISRC
	})
}

// Album returns the remembered tracks of a Spotify album.
func (c *Cache) Album(id string) (AlbumEntry, bool) {
	if c == nil {
		return AlbumEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.f.Albums[id]
	return a, ok
}

// SetAlbum remembers the tracks of a Spotify album.
func (c *Cache) SetAlbum(id string, a AlbumEntry) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f.Albums == nil {
		c.f.Albums = map[string]AlbumEntry{}
	}
	a.At = time.Now()
	c.f.Albums[id], c.dirty = a, true
}

// MarkDue makes the next run look a release up again, even if the last answer still holds.
func (c *Cache) MarkDue(t hardstyle.Track) {
	c.update(t, func(e *Entry) { e.At = time.Time{} })
}

// Force sets the Spotify track of a release by hand. It wins over whatever the search would find, and it is
// not searched again.
func (c *Cache) Force(t hardstyle.Track, uri string) {
	c.update(t, func(e *Entry) {
		e.Forced = uri
		e.URI, e.Extra = uri, nil
		e.Album, e.AlbumID, e.DaysOff = false, "", 0
		e.At, e.Reason = time.Now(), ""
	})
}

// Forget drops the Spotify track of a release, so the next run looks it up again. The refused tracks stay.
func (c *Cache) Forget(t hardstyle.Track) {
	c.update(t, func(e *Entry) {
		e.URI, e.Extra, e.Album, e.AlbumID, e.DaysOff = "", nil, false, "", 0
		e.At, e.SpotifyDate, e.Reason = time.Time{}, "", ""
	})
}

// Reject refuses a Spotify track for a release for good and drops the current match, so the next run looks for
// another one. The same track is remembered only once.
func (c *Cache) Reject(t hardstyle.Track, uri string) {
	c.update(t, func(e *Entry) {
		if uri != "" && !slices.Contains(e.Rejected, uri) {
			e.Rejected = append(e.Rejected, uri)
		}
		drop := map[string]bool{uri: uri != ""}
		for _, u := range e.Rejected {
			drop[u] = true
		}
		if drop[e.URI] {
			// The whole match goes: a remix remembered next to a refused track would bring it back.
			e.URI, e.Extra, e.Album, e.AlbumID, e.DaysOff = "", nil, false, "", 0
			e.At, e.SpotifyDate, e.Reason = time.Time{}, "", ""
			return
		}
		kept := e.Extra[:0]
		for _, u := range e.Extra {
			if !drop[u] {
				kept = append(kept, u)
			}
		}
		e.Extra = kept
	})
}

func (c *Cache) SetMissing(t hardstyle.Track, reason, spotifyDate string) {
	c.update(t, func(e *Entry) {
		e.URI, e.Extra, e.Album, e.AlbumID, e.DaysOff = "", nil, false, "", 0
		e.At, e.SpotifyDate, e.Reason = time.Now(), spotifyDate, reason
		e.Tries++
	})
}

// DatesByURI returns the release date of every known Spotify track.
func (c *Cache) DatesByURI() map[string]string {
	dates := map[string]string{}
	if c == nil {
		return dates
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.f.Tracks {
		if e.URI != "" && e.Date != "" {
			dates[e.URI] = e.Date
			for _, u := range e.Extra {
				dates[u] = e.Date
			}
		}
	}
	return dates
}

// Playlist returns the remembered state of the playlist, or nil.
func (c *Cache) Playlist(id string) *PlaylistSnapshot {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f.Playlist == nil || c.f.Playlist.ID != id {
		return nil
	}
	p := *c.f.Playlist
	p.URIs = append([]string(nil), p.URIs...)
	return &p
}

func (c *Cache) SetPlaylist(p PlaylistSnapshot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.f.Playlist, c.dirty = &p, true
}

// Description returns the description written last to the playlist.
func (c *Cache) Description(id string) string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f.Description == nil || c.f.Description.ID != id {
		return ""
	}
	return c.f.Description.Text
}

func (c *Cache) SetDescription(id, text string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.f.Description, c.dirty = &describedPlaylist{ID: id, Text: text}, true
}

// AdoptLegacyBlock moves a block saved before several apps were supported to the app with the given client id.
func (c *Cache) AdoptLegacyBlock(clientID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f.BlockedUntil.IsZero() {
		return
	}
	if c.f.Blocked == nil {
		c.f.Blocked = map[string]time.Time{}
	}
	if c.f.BlockedUntil.After(c.f.Blocked[clientID]) {
		c.f.Blocked[clientID] = c.f.BlockedUntil
	}
	c.f.BlockedUntil, c.dirty = time.Time{}, true
}

// Block remembers that Spotify blocks an app until a time, so no run asks with it before.
func (c *Cache) Block(clientID string, until time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f.Blocked == nil {
		c.f.Blocked = map[string]time.Time{}
	}
	c.f.Blocked[clientID], c.dirty = until, true
}

// BlockedUntil returns when the block of an app ends; zero if there is none.
func (c *Cache) BlockedUntil(clientID string) time.Time {
	if c == nil {
		return time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	until := c.f.Blocked[clientID]
	if time.Now().After(until) {
		return time.Time{}
	}
	return until
}

// Save writes the cache, if something changed.
func (c *Cache) Save() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}
	for k, e := range c.f.Tracks {
		if !e.At.IsZero() && time.Since(e.At) > cacheMaxAge {
			delete(c.f.Tracks, k)
		}
	}
	for id, a := range c.f.Albums {
		if time.Since(a.At) > cacheMaxAge {
			delete(c.f.Albums, id)
		}
	}
	data, err := json.Marshal(c.f)
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(c.path), "."+filepath.Base(c.path)+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return err
	}
	c.dirty = false
	return nil
}
