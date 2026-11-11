// Package hdrr reads the release radar playlists of hdrr.info (Hard Dance Release Radar), the third source besides
// hardstyle.com and hardtunes.com: HDRR collects every new release of a genre on Spotify, so it fills in the
// releases the two shops do not list.
package hdrr

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/i18n"
)

// Track is one entry of a release radar playlist: title, artists and the day of the release.
type Track struct {
	hardstyle.Track
	Date string // the day of the release, yyyy-mm-dd; empty when the page names none
}

// IDPrefix starts the ID of every release of hdrr.info, so it cannot be mistaken for the ID of another site in
// the cache, and the window can tell where a release comes from.
const IDPrefix = "hdrr:"

// Base is the address of hdrr.info; tests point it at a local server.
var Base = "https://www.hdrr.info"

// DetailDelay is the pause between two track-page requests to hdrr.info. Tests shorten it.
var DetailDelay = time.Second

// Playlists are the release radar playlists of hdrr.info that are registered as a source. A playlist lives at
// https://www.hdrr.info/playlists/<playlist>-release-radar
var Playlists = []string{"hardstyle", "uptempo", "hardcore", "hard-techno", "frenchcore", "happy-hardcore"}

// labels name a playlist for the screen and the log, with the genre names of hardstyle.com, so GENRE_MIN counts
// the same genre of every site together.
var labels = map[string]string{
	"hardstyle":      "Hardstyle",
	"uptempo":        "Uptempo",
	"hardcore":       "Hardcore",
	"hard-techno":    "Hard Techno",
	"frenchcore":     "Frenchcore",
	"happy-hardcore": "UK/Happy Hardcore",
}

// GenreLabel names a playlist for the screen and the log.
func GenreLabel(playlist string) string {
	if l, ok := labels[playlist]; ok {
		return l
	}
	return playlist
}

// PlaylistURL is the address of a release radar playlist.
func PlaylistURL(playlist string) string {
	return fmt.Sprintf("%s/playlists/%s-release-radar", Base, playlist)
}

// ErrNoResults means the playlist has no tracks.
var ErrNoResults = errors.New("no results")

// FetchPlaylist reads a release radar playlist, newest first. The whole playlist is on one page (a few weeks of
// releases). A request that fails for a passing reason is repeated (hardstyle.Retries).
func FetchPlaylist(ctx context.Context, client *http.Client, playlist string) ([]Track, error) {
	u := PlaylistURL(playlist)
	body, _, err := hardstyle.Get(ctx, client, u)
	if err != nil {
		return nil, err
	}
	tracks, err := parsePlaylist(string(body), playlist)
	if err != nil && !errors.Is(err, ErrNoResults) {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	return tracks, err
}

// parsePlaylist reads the tracklist of a playlist page: an ol with data-list="playlist", one li per track with
// a link to the track page, a line of artists and a time element. The playlist is in the order of Spotify, not
// strictly by day, so it is sorted newest first; equal days keep their order.
func parsePlaylist(page, playlist string) ([]Track, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil, err
	}
	list := find(doc, func(n *html.Node) bool { return isElem(n, "ol") && attr(n, "data-list") == "playlist" })
	if list == nil {
		return nil, fmt.Errorf("%s", i18n.T("keine Trackliste gefunden, hat sich die Seite geändert?", "no tracklist found, did the site change?"))
	}
	var tracks []Track
	for _, li := range findAll(list, func(n *html.Node) bool { return isElem(n, "li") && hasAttr(n, "data-position") }) {
		if t, ok := parseItem(li, playlist); ok {
			tracks = append(tracks, t)
		}
	}
	if len(tracks) == 0 {
		return nil, ErrNoResults
	}
	slices.SortStableFunc(tracks, func(a, b Track) int { return cmp.Compare(b.Date, a.Date) })
	return tracks, nil
}

// parseItem reads one track of the list.
func parseItem(li *html.Node, playlist string) (Track, bool) {
	link := find(li, func(n *html.Node) bool { return isElem(n, "a") && strings.HasPrefix(attr(n, "href"), "/tracks/") })
	artists := find(li, func(n *html.Node) bool { return isElem(n, "p") })
	if link == nil || artists == nil {
		return Track{}, false
	}
	title := hardstyle.Clean(text(link))
	artist := hardstyle.Clean(strings.ReplaceAll(text(artists), string(rune(0xA0)), " "))
	if title == "" || artist == "" {
		return Track{}, false
	}
	href := attr(link, "href")
	date := ""
	if tm := find(li, func(n *html.Node) bool { return isElem(n, "time") }); tm != nil {
		if d, err := time.Parse("2006-01-02", firstN(attr(tm, "datetime"), 10)); err == nil {
			date = d.Format("2006-01-02")
		}
	}
	title, version := splitVersion(title)
	return Track{
		Track: hardstyle.Track{
			ID:      IDPrefix + strings.TrimPrefix(href, "/tracks/"),
			Path:    Base + href,
			Title:   title,
			Version: version,
			Artist:  artist,
			Genre:   GenreLabel(playlist),
		},
		Date: date,
	}, true
}

// versionWords tell a version in brackets ("(Drokz Remix)") from a part of the title ("(the odyssey)").
var versionWords = regexp.MustCompile(`(?i)\b(mix|remix|edit|flip|bootleg|version|vip|rework|remaster(ed)?|cut|refix|instrumental)\b`)

// splitVersion splits the version from the title the way Spotify writes it ("Like A Bomb - Extended Mix",
// "Battles (Drokz Remix)"), so a release is named like on hardstyle.com, where the version is a line of its own.
func splitVersion(title string) (string, string) {
	if i := strings.LastIndex(title, " - "); i > 0 {
		if v := hardstyle.Clean(title[i+3:]); v != "" {
			return hardstyle.Clean(title[:i]), v
		}
	}
	if i := strings.LastIndex(title, "("); i > 0 && strings.HasSuffix(title, ")") {
		if v := hardstyle.Clean(title[i+1 : len(title)-1]); versionWords.MatchString(v) {
			return hardstyle.Clean(title[:i]), v
		}
	}
	return title, ""
}

// DetailFetcher reads the ISRC, the length and the day of a release from its track page on hdrr.info, whose JSON-LD
// is written like that of hardstyle.com. The requests are spaced out.
type DetailFetcher struct {
	hc    *http.Client
	delay time.Duration
	mu    sync.Mutex
	next  time.Time
}

func NewDetailFetcher(hc *http.Client, delay time.Duration) *DetailFetcher {
	return &DetailFetcher{hc: hc, delay: delay}
}

// Fetch reads the track page of the release. A page without a day keeps the day of the list.
func (f *DetailFetcher) Fetch(ctx context.Context, t Track) (hardstyle.Details, error) {
	if !strings.HasPrefix(t.Path, "http://") && !strings.HasPrefix(t.Path, "https://") {
		return hardstyle.Details{}, errors.New(i18n.T("keine Adresse der Detailseite", "no address of the detail page"))
	}
	f.mu.Lock()
	wait := max(0, time.Until(f.next))
	f.next = time.Now().Add(wait + f.delay)
	f.mu.Unlock()
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return hardstyle.Details{}, ctx.Err()
	}
	body, _, err := hardstyle.Get(ctx, f.hc, t.Path)
	if err != nil {
		return hardstyle.Details{}, err
	}
	d, err := hardstyle.ParseDetails(body)
	if err != nil {
		if t.Date == "" {
			return hardstyle.Details{}, err
		}
		d = hardstyle.Details{Date: t.Date}
	}
	return d, nil
}

func firstN(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

func isElem(n *html.Node, tag string) bool { return n.Type == html.ElementNode && n.Data == tag }

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	return slices.ContainsFunc(n.Attr, func(a html.Attribute) bool { return a.Key == key })
}

func find(n *html.Node, match func(*html.Node) bool) *html.Node {
	if match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := find(c, match); r != nil {
			return r
		}
	}
	return nil
}

func findAll(n *html.Node, match func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	if match(n) {
		return append(out, n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out = append(out, findAll(c, match)...)
	}
	return out
}

func text(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(text(c))
	}
	return sb.String()
}
