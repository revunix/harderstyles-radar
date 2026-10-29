package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"hardstylesync/internal/cache"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/match"
	"hardstylesync/internal/spotify"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeSpotify implements just enough of the Spotify API for Sync. Tracks are called T1, T2, ...; every one of
// them exists on Spotify unless it is in missing, with the release date 2026-10-29 unless dates says otherwise.
type fakeSpotify struct {
	mu       sync.Mutex
	playlist []string
	snap     int
	missing  map[string]bool
	dates    map[string]string
	limited  bool // answer every search with a day long rate limit
	// limitAfter turns the rate limit on after that many searches (0: never).
	limitAfter int
	// failTitles are titles whose search always ends with a 500; failNext makes the next searches fail.
	failTitles map[string]bool
	failNext   int
	failPost   bool // answer inserts with a 500
	// catalog is a made-up Spotify: if it is not empty, searches answer from it only.
	catalog []fakeTrack
	posts   int // inserts tried

	description       string
	descriptionWrites int
	failDescription   bool
	searches          int
	albumReads        int  // reads of the track list of an album
	failAlbums        bool // answer the track lists of albums with a 403
	itemReads         int
	snapReads         int
	ops               []string // the writes, e.g. "POST@0:spotify:track:N1"
}

func uri(title string) string { return "spotify:track:" + title }

// fakeTrack is a track of the made-up Spotify.
type fakeTrack struct {
	URI     string
	Name    string
	Artists []string
	Date    string
	ISRC    string
	Key     string // a search whose text contains this (lower case, without accents) finds the track
	// The album the track is on; without one, it is a single of its own.
	AlbumID, AlbumName, AlbumType string
	AlbumTotal                    int
	Hidden                        bool // only on the track list of its album, no search finds it
}

// album returns the album of the track as Spotify's search names it.
func (c fakeTrack) album() string {
	id, name, typ, total := c.AlbumID, c.AlbumName, c.AlbumType, c.AlbumTotal
	if id == "" {
		id, name, typ, total = "single-"+c.URI, c.Name, "single", 1
	}
	return fmt.Sprintf(`{"id":%q,"name":%q,"album_type":%q,"total_tracks":%d,"release_date":%q,"release_date_precision":"day"}`, id, name, typ, total, c.Date)
}

func (f *fakeSpotify) setPlaylist(titles ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.playlist = nil
	for _, t := range titles {
		f.playlist = append(f.playlist, uri(t))
	}
	f.snap++
}

func (f *fakeSpotify) titles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, u := range f.playlist {
		out = append(out, strings.TrimPrefix(u, "spotify:track:"))
	}
	return out
}

func (f *fakeSpotify) state() (searches, itemReads, snapReads int, ops []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searches, f.itemReads, f.snapReads, append([]string(nil), f.ops...)
}

func (f *fakeSpotify) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "10" {
			t.Errorf("search limit = %q, want 10", r.URL.Query().Get("limit"))
		}
		fields := strings.Fields(strings.TrimPrefix(r.URL.Query().Get("q"), "track:"))
		title := ""
		if len(fields) > 0 {
			title = fields[0]
		}
		f.mu.Lock()
		f.searches++
		limited := f.limited || (f.limitAfter > 0 && f.searches > f.limitAfter)
		fail := f.failTitles[title] || f.failNext > 0
		if f.failNext > 0 {
			f.failNext--
		}
		missing, date := f.missing[title], f.dates[title]
		catalog := f.catalog
		f.mu.Unlock()
		if len(catalog) > 0 && !limited && !fail {
			q := strings.ToLower(match.Fold(r.URL.Query().Get("q")))
			var items []string
			for _, c := range catalog {
				if c.Hidden {
					continue
				}
				if (strings.HasPrefix(q, "isrc:") && strings.EqualFold(c.ISRC, strings.TrimPrefix(q, "isrc:"))) ||
					(!strings.HasPrefix(q, "isrc:") && strings.Contains(q, c.Key)) {
					var names []string
					for _, a := range c.Artists {
						names = append(names, fmt.Sprintf(`{"name":%q}`, a))
					}
					items = append(items, fmt.Sprintf(`{"uri":%q,"name":%q,"artists":[%s],"album":%s}`,
						c.URI, c.Name, strings.Join(names, ","), c.album()))
				}
			}
			fmt.Fprintf(w, `{"tracks":{"items":[%s]}}`, strings.Join(items, ","))
			return
		}
		if limited {
			w.Header().Set("Retry-After", "85181")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if date == "" {
			date = "2026-10-29"
		}
		if missing {
			fmt.Fprint(w, `{"tracks":{"items":[]}}`)
			return
		}
		fmt.Fprintf(w, `{"tracks":{"items":[{"uri":%q,"name":%q,"artists":[{"name":"A"}],"album":{"release_date":%q,"release_date_precision":"day"}}]}}`,
			uri(title), title, date)
	})
	mux.HandleFunc("/albums/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/albums/"), "/tracks")
		f.mu.Lock()
		f.albumReads++
		failing := f.failAlbums
		var items []map[string]any
		for _, c := range f.catalog {
			if c.AlbumID == id {
				var artists []map[string]string
				for _, a := range c.Artists {
					artists = append(artists, map[string]string{"name": a})
				}
				items = append(items, map[string]any{"uri": c.URI, "name": c.Name, "artists": artists})
			}
		}
		f.mu.Unlock()
		if failing {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items}) // one page, no "next"
	})
	mux.HandleFunc("/playlists/p", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodPut {
			var b struct{ Description string }
			json.NewDecoder(r.Body).Decode(&b)
			if f.failDescription {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			f.description = b.Description
			f.descriptionWrites++
			return
		}
		f.snapReads++
		fmt.Fprintf(w, `{"snapshot_id":"s%d"}`, f.snap)
	})
	mux.HandleFunc("/playlists/p/items", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			f.itemReads++
			items := []map[string]any{}
			for _, u := range f.playlist {
				items = append(items, map[string]any{"added_at": "2026-01-01T00:00:00Z", "item": map[string]string{"uri": u}})
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items}) // one page, no "next"
			return
		case http.MethodPost:
			f.posts++
			if f.failPost {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var b struct {
				URIs     []string
				Position *int
			}
			json.NewDecoder(r.Body).Decode(&b)
			at := len(f.playlist)
			if b.Position != nil {
				at = *b.Position
			}
			f.ops = append(f.ops, fmt.Sprintf("POST@%d:%s", at, strings.Join(b.URIs, ",")))
			f.playlist = slices.Insert(f.playlist, at, b.URIs...)
		case http.MethodPut:
			var b struct{ URIs []string }
			json.NewDecoder(r.Body).Decode(&b)
			f.ops = append(f.ops, "PUT:"+strings.Join(b.URIs, ","))
			f.playlist = append([]string(nil), b.URIs...)
		case http.MethodDelete:
			var b struct{ Items []struct{ URI string } }
			json.NewDecoder(r.Body).Decode(&b)
			drop := map[string]bool{}
			for _, i := range b.Items {
				drop[i.URI] = true
			}
			f.ops = append(f.ops, fmt.Sprintf("DELETE:%d", len(b.Items)))
			f.playlist = slices.DeleteFunc(f.playlist, func(u string) bool { return drop[u] })
		}
		f.snap++
		fmt.Fprintf(w, `{"snapshot_id":"s%d"}`, f.snap)
	})
	return mux
}

func newFakeClient(t *testing.T, f *fakeSpotify) *spotify.Client {
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	sp := spotify.New(srv.Client(), "id", "secret", "refresh", spotify.WithEndpoints(srv.URL, srv.URL+"/token"), spotify.WithRetryWait(0))
	return sp
}

func newCache(t *testing.T) (*cache.Cache, string) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c, err := cache.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return c, path
}

// site returns releases in the order of the site's list.

// site returns releases in the order of the site's list.
func site(titles ...string) []hardstyle.Track {
	var out []hardstyle.Track
	for _, title := range titles {
		out = append(out, hardstyle.Track{ID: "id-" + title, Title: title, Artist: "A", Genre: "Hardstyle"})
	}
	return out
}

func opts(dates map[string]string) Options {
	return Options{
		MaxTracks: 500, MaxSearches: 100, DateTolerance: 14,
		FetchDetails: func(_ context.Context, t hardstyle.Track) (hardstyle.Details, error) {
			if d, ok := dates[t.Title]; ok {
				return hardstyle.Details{Date: d}, nil
			}
			return hardstyle.Details{Date: "2026-10-29"}, nil
		},
	}
}

func mustSync(t *testing.T, sp *spotify.Client, releases []hardstyle.Track, o Options, c *cache.Cache) Result {
	t.Helper()
	res, err := Sync(context.Background(), sp, "p", releases, o, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// fakeSite is a small hardstyle.com: a list of tracks with 3 per page, and a detail page for each.
type fakeSite struct {
	mu     sync.Mutex
	tracks []hardstyle.Track
	hits   map[string]int
	dates  map[string]hardstyle.Details
}

const perPage = 3

func (s *fakeSite) hit(what string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hits == nil {
		s.hits = map[string]int{}
	}
	s.hits[what]++
}

func (s *fakeSite) count(what string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[what]
}

func (s *fakeSite) start(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		tracks := append([]hardstyle.Track(nil), s.tracks...)
		s.mu.Unlock()
		if r.URL.Path == "/en/tracks" {
			s.hit("list")
			var page int
			fmt.Sscan(r.URL.Query().Get("page"), &page)
			from := (page - 1) * perPage
			if from >= len(tracks) {
				fmt.Fprint(w, "<html><body>No results found, please try another selection.</body></html>")
				return
			}
			var b strings.Builder
			b.WriteString(`<html><body><div class="listViewWrapper">`)
			for _, tr := range tracks[from:min(from+perPage, len(tracks))] {
				fmt.Fprintf(&b, `<div class="track listView" data-track-id=%q><a class="linkTitle trackTitle" href="/en/tracks/%s/x"><span class="innerLink">%s</span></a>`, tr.ID, tr.ID, tr.Title)
				if tr.Version != "" {
					fmt.Fprintf(&b, `<a class="linkTitle" href="/x"> %s </a>`, tr.Version)
				}
				fmt.Fprintf(&b, `<span class="artists"><a><span class="innerLink">%s</span></a></span></div>`, tr.Artist)
			}
			b.WriteString(`</div></body></html>`)
			fmt.Fprint(w, b.String())
			return
		}
		s.hit("detail")
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/en/tracks/"), "/")[0]
		d := hardstyle.Details{Date: "2026-10-29"}
		s.mu.Lock()
		if x, ok := s.dates[id]; ok {
			d = x
		}
		s.mu.Unlock()
		fmt.Fprintf(w, `<script type="application/ld+json">{"catalogNumber": %q, "datePublished": "%sT00:00:00+01:00", "isrcCode": %q}</script>`, d.Catalog, d.Date, d.ISRC)
	}))
	t.Cleanup(srv.Close)
	before := hardstyle.Base
	hardstyle.Base = srv.URL
	t.Cleanup(func() { hardstyle.Base = before })
	return srv
}

func siteTracks(n int) []hardstyle.Track {
	var out []hardstyle.Track
	for i := 1; i <= n; i++ {
		out = append(out, hardstyle.Track{ID: fmt.Sprintf("id%d", i), Title: fmt.Sprintf("T%d", i), Artist: "A"})
	}
	return out
}

// detailsOf returns a FetchDetails that gives a date and an ISRC per title.
func detailsOf(m map[string]hardstyle.Details) func(context.Context, hardstyle.Track) (hardstyle.Details, error) {
	return func(_ context.Context, t hardstyle.Track) (hardstyle.Details, error) {
		key := t.Title
		if t.Version != "" {
			key += "|" + t.Version
		}
		if d, ok := m[key]; ok {
			return d, nil
		}
		return hardstyle.Details{Date: "2026-10-29"}, nil
	}
}

// the page past the last one is a request too
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
