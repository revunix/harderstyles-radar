package hardstyle

import (
	"context"
	"errors"
	"fmt"
	"hardstylesync/internal/i18n"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

// Track is one entry of the hardstyle.com track list.
type Track struct {
	ID    string
	Path  string // address of the detail page, e.g. /en/tracks/<id>/<slug>
	Title string
	// Version is the second line under the title on the site: "Extended Mix", "Fiesta Mix", "Some Remix".
	Version string
	Artist  string
	Genre   string
}

var spaces = regexp.MustCompile(`\s+`)

// ErrNoResults means the page is past the last page of the genre (or the genre is empty).
var ErrNoResults = errors.New("no results")

// Base is the address of hardstyle.com; tests point it at a local server.
var Base = "https://hardstyle.com"

// The pauses between two requests to hardstyle.com, to be friendly to the site. Tests shorten them.
var (
	PageDelay   = time.Second
	DetailDelay = time.Second
)

// AnyGenre asks for the general list of hardstyle.com, whatever the genre.
const AnyGenre = ""

// GenreLabel names a genre filter for the screen and the log.
func GenreLabel(genre string) string {
	if genre == AnyGenre {
		return i18n.T("alle Genres", "all genres")
	}
	return genre
}

// trackListURL is the address of a page of the track list. Without a genre it is the general list.
func trackListURL(genre string, page int) string {
	if genre == AnyGenre {
		return fmt.Sprintf("%s/en/tracks?page=%d", Base, page)
	}
	return fmt.Sprintf("%s/en/tracks?genre=%s&page=%d", Base, url.QueryEscape(genre), page)
}

// fetchPage reads one page of the track list. ErrNoResults means the page is past the last one.
func fetchPage(ctx context.Context, client *http.Client, genre string, page int) ([]Track, error) {
	u := trackListURL(genre, page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; hardstyle-playlist-sync)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hardstyle.com %s: HTTP %d", u, resp.StatusCode)
	}
	tracks, err := parseTrackList(string(body), genre)
	if err != nil && !errors.Is(err, ErrNoResults) {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	return tracks, err
}

// FetchReleases returns the newest tracks of a genre (or of the general list), newest first.
// The list at hardstyle.com/en/tracks is sorted by release date, 30 tracks per page.
func FetchReleases(ctx context.Context, client *http.Client, genre string, pages int) ([]Track, error) {
	return FetchUntilKnown(ctx, client, genre, pages, nil)
}

// FetchUntilKnown reads the list page by page and stops after the first page that has a release the caller
// already knows: everything newer than that has been read then. At most maxPages pages are read. Without
// known, all maxPages pages are read. One page is all a check for new tracks has to read, most of the time.
func FetchUntilKnown(ctx context.Context, client *http.Client, genre string, maxPages int, known func(Track) bool) ([]Track, error) {
	var out []Track
	for page := 1; page <= maxPages; page++ {
		if page > 1 {
			select {
			case <-time.After(PageDelay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		tracks, err := fetchPage(ctx, client, genre, page)
		if errors.Is(err, ErrNoResults) {
			break // small genres have fewer pages than requested
		}
		if err != nil {
			return nil, err
		}
		out = append(out, tracks...)
		if known != nil && slices.ContainsFunc(tracks, known) {
			break
		}
	}
	return out, nil
}

// parseTrackList reads the list view of a /en/tracks page.
// The page renders every track twice (block view and list view), so only the list view is used.
func parseTrackList(page, genre string) ([]Track, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil, err
	}
	list := find(doc, func(n *html.Node) bool { return isElem(n, "div") && hasClass(n, "listViewWrapper") })
	if list == nil {
		if strings.Contains(page, "No results found") {
			return nil, ErrNoResults
		}
		return nil, fmt.Errorf("%s", i18n.T("keine Titelliste gefunden, hat sich die Seite wieder geändert?", "no track list found, did the site change again?"))
	}

	var tracks []Track
	for _, n := range findAll(list, func(n *html.Node) bool {
		return isElem(n, "div") && hasClass(n, "track") && hasClass(n, "listView")
	}) {
		title := find(n, func(n *html.Node) bool { return isElem(n, "a") && hasClass(n, "trackTitle") })
		// The line under the title: a link with the class linkTitle, but not the title itself.
		version := find(n, func(n *html.Node) bool {
			return isElem(n, "a") && hasClass(n, "linkTitle") && !hasClass(n, "trackTitle")
		})
		artists := find(n, func(n *html.Node) bool { return isElem(n, "span") && hasClass(n, "artists") })
		if title == nil || artists == nil {
			continue
		}
		// Not every artist is a link ("<a>Act of Rage</a>, Spitnoise and Nolz"), so take the whole text.
		t := Clean(text(title))
		a := strings.ReplaceAll(Clean(text(artists)), " ,", ",")
		if t == "" || a == "" {
			continue
		}
		v := ""
		if version != nil {
			v = Clean(text(version))
		}
		tracks = append(tracks, Track{ID: attr(n, "data-track-id"), Path: attr(title, "href"), Title: t, Version: v, Artist: a, Genre: genre})
	}
	return tracks, nil
}

// Clean decodes whitespace noise in scraped text.
func Clean(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }

func isElem(n *html.Node, tag string) bool { return n.Type == html.ElementNode && n.Data == tag }

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
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
		out = append(out, n)
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

var (
	datePublished = regexp.MustCompile(`"datePublished"\s*:\s*"(\d{4}-\d{2}-\d{2})`)
	isrcCode      = regexp.MustCompile(`"isrcCode"\s*:\s*"([A-Za-z0-9-]+)"`)
	catalogNumber = regexp.MustCompile(`"catalogNumber"\s*:\s*"([^"]+)"`)
)

// Details is what the page of a release on hardstyle.com tells besides the list.
type Details struct {
	Date string // release date, yyyy-mm-dd
	ISRC string // international code of the recording; Spotify can search for it. May be empty.
	// Catalog is the catalog number of the release the track is part of: all tracks of an album have the same
	// one ("MINUS288"), whoever the artist. May be empty.
	Catalog string
}

// DetailFetcher reads the release date and the ISRC of a track from its page on hardstyle.com. They never
// change, so the cache keeps them; the requests are spaced out, to be friendly to the site.
type DetailFetcher struct {
	hc    *http.Client
	delay time.Duration
	mu    sync.Mutex
	next  time.Time
}

func NewDetailFetcher(hc *http.Client, delay time.Duration) *DetailFetcher {
	return &DetailFetcher{hc: hc, delay: delay}
}

// Fetch reads the page of the track.
func (f *DetailFetcher) Fetch(ctx context.Context, t Track) (Details, error) {
	if t.Path == "" {
		return Details{}, errors.New(i18n.T("keine Adresse der Detailseite", "no address of the detail page"))
	}
	f.mu.Lock()
	wait := max(0, time.Until(f.next))
	f.next = time.Now().Add(wait + f.delay)
	f.mu.Unlock()
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return Details{}, ctx.Err()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Base+t.Path, nil)
	if err != nil {
		return Details{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; hardstyle-playlist-sync)")
	resp, err := f.hc.Do(req)
	if err != nil {
		return Details{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Details{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Details{}, err
	}
	return parseDetails(body)
}

// parseDetails reads the date, the ISRC and the catalog number from the data of a track page (JSON-LD).
func parseDetails(page []byte) (Details, error) {
	m := datePublished.FindSubmatch(page)
	if m == nil {
		return Details{}, errors.New(i18n.T("kein Veröffentlichungsdatum auf der Seite", "no release date on the page"))
	}
	d := Details{Date: string(m[1])}
	if i := isrcCode.FindSubmatch(page); i != nil {
		d.ISRC = strings.ToUpper(strings.ReplaceAll(string(i[1]), "-", ""))
	}
	if c := catalogNumber.FindSubmatch(page); c != nil {
		d.Catalog = strings.ToUpper(Clean(string(c[1])))
	}
	return d, nil
}
