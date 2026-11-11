package hdrr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"hardstylesync/internal/hardstyle"
)

func init() { hardstyle.RetryDelay = 0 }

// item is one track of a playlist as hdrr.info renders it; artists without a page are plain text.
func item(pos int, slug, title, artists, datetime string) string {
	return fmt.Sprintf(`<li data-position="%d" class="flex items-center gap-3 py-3 sm:gap-4"><span class="w-7 shrink-0">%d</span><div class="min-w-0 flex-1"><a href="/tracks/%s" aria-label="%s" class="block truncate">%s</a><p class="mt-1 truncate text-[13px] text-fg-muted">%s</p></div><time datetime="%s" class="shrink-0">x</time></li>`,
		pos, pos, slug, title, title, artists, datetime)
}

// fixture is a playlist page: a header with a Spotify link, then the tracklist, not strictly by day.
var fixture = `<html><body><header><a href="https://open.spotify.com/playlist/x">Open in Spotify</a></header><section><ol class="divide-y" data-list="playlist">` +
	item(1, "the-power-is-ours", "The Power Is Ours", `<a href="/artists/toneshifterz">Toneshifterz</a>`, "2026-11-10T00:00:00.000Z") +
	item(2, "man-of-god", "Man of God - Radio Edit", `<a href="/artists/mbk">MBK</a>, Omega Hart`, "2026-11-11T00:00:00.000Z") +
	item(3, "battles-3", "Battles (Drokz Remix)", `<a href="/artists/steen">Steen</a>, <a href="/artists/drokz">Drokz</a>`, "2026-10-20T00:00:00.000Z") +
	item(4, "sirens", "SIRENS (the odyssey)", `<a href="/artists/crypton">Crypton</a>`, "2026-11-10T00:00:00.000Z") +
	`</ol></section></body></html>`

func TestParsePlaylist(t *testing.T) {
	tracks, err := parsePlaylist(fixture, "hardcore")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tr := range tracks {
		got = append(got, fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s", tr.ID, tr.Title, tr.Version, tr.Artist, tr.Date, tr.Genre, tr.Path))
	}
	want := []string{
		"hdrr:man-of-god|Man of God|Radio Edit|MBK, Omega Hart|2026-11-11|Hardcore|" + Base + "/tracks/man-of-god",
		"hdrr:the-power-is-ours|The Power Is Ours||Toneshifterz|2026-11-10|Hardcore|" + Base + "/tracks/the-power-is-ours",
		"hdrr:sirens|SIRENS (the odyssey)||Crypton|2026-11-10|Hardcore|" + Base + "/tracks/sirens",
		"hdrr:battles-3|Battles|Drokz Remix|Steen, Drokz|2026-10-20|Hardcore|" + Base + "/tracks/battles-3",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got\n%v\nwant (newest first, equal days in list order)\n%v", got, want)
	}
}

func TestParsePlaylistSiteChanged(t *testing.T) {
	if _, err := parsePlaylist(`<html><body><p>nothing</p></body></html>`, "hardstyle"); err == nil || errors.Is(err, ErrNoResults) {
		t.Errorf("a page without the tracklist must be an error, got %v", err)
	}
	if _, err := parsePlaylist(`<html><body><ol data-list="playlist"></ol></body></html>`, "hardstyle"); !errors.Is(err, ErrNoResults) {
		t.Errorf("an empty playlist is ErrNoResults, got %v", err)
	}
}

func TestGenreLabelMatchesHardstyleCom(t *testing.T) {
	if got := GenreLabel("happy-hardcore"); got != "UK/Happy Hardcore" {
		t.Errorf("happy-hardcore = %q, want the hardstyle.com name so GENRE_MIN counts it together", got)
	}
	if got := GenreLabel("hard-techno"); got != "Hard Techno" {
		t.Errorf("hard-techno = %q", got)
	}
}

func TestFetchPlaylistAndDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/playlists/hardstyle-release-radar":
			fmt.Fprint(w, fixture)
		case "/tracks/the-power-is-ours":
			fmt.Fprint(w, `<script type="application/ld+json">{"@type":"MusicRecording","datePublished":"2026-11-11","duration":"PT4M26S","isrcCode":"NLH2L2600389","sameAs":["https://open.spotify.com/track/63KDXpzV56UOo3Vumj8xq7"]}</script>`)
		case "/tracks/sirens":
			fmt.Fprint(w, `<html></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	before := Base
	Base = srv.URL
	defer func() { Base = before }()

	tracks, err := FetchPlaylist(context.Background(), srv.Client(), "hardstyle")
	if err != nil || len(tracks) != 4 {
		t.Fatalf("FetchPlaylist: %d tracks, %v", len(tracks), err)
	}
	if _, err := FetchPlaylist(context.Background(), srv.Client(), "nope"); err == nil {
		t.Error("a missing playlist must be an error")
	}

	f := NewDetailFetcher(srv.Client(), 0)
	byID := map[string]Track{}
	for _, tr := range tracks {
		byID[tr.ID] = tr
	}
	d, err := f.Fetch(context.Background(), byID["hdrr:the-power-is-ours"])
	if err != nil || d.ISRC != "NLH2L2600389" || d.Duration != 266 || d.Date != "2026-11-11" {
		t.Errorf("details = %+v, %v", d, err)
	}
	// A page without data keeps the day of the list.
	d, err = f.Fetch(context.Background(), byID["hdrr:sirens"])
	if err != nil || d.Date != "2026-11-10" || d.ISRC != "" {
		t.Errorf("details without data = %+v, %v", d, err)
	}
}

func TestSplitVersion(t *testing.T) {
	for _, c := range []struct{ in, title, version string }{
		{"Like A Bomb - Extended Mix", "Like A Bomb", "Extended Mix"},
		{"Lekker Genieten - Gezellige Uptempo Remix", "Lekker Genieten", "Gezellige Uptempo Remix"},
		{"Misery (Hyper Flip)", "Misery", "Hyper Flip"},
		{"SIRENS (the odyssey)", "SIRENS (the odyssey)", ""},
		{"Plain", "Plain", ""},
	} {
		if title, version := splitVersion(c.in); title != c.title || version != c.version {
			t.Errorf("splitVersion(%q) = %q, %q; want %q, %q", c.in, title, version, c.title, c.version)
		}
	}
}
