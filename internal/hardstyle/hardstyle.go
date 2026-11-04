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
	"strconv"
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
	// Label is the record label as the list names it ("I AM HARDSTYLE"); may be empty.
	Label string
	// Duration is the length in seconds as the list names it ("04:41"); 0 when unknown.
	Duration int
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

// A request that fails for a passing reason (no connection, a server error, too many requests) is repeated
// Retries times, after RetryDelay, doubled before every further try. Tests shorten the delay.
var (
	Retries    = 2
	RetryDelay = 2 * time.Second
)

// maxBody is the most of a page that is read; the pages are far smaller.
const maxBody = 4 << 20

// StatusError is the answer of a site that is not 200 OK.
type StatusError struct {
	URL  string
	Code int
}

func (e *StatusError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.URL, e.Code) }

// transient reports whether the answer may be different on the next try: a server error or too many
// requests. Other answers (404, ...) stay the same.
func (e *StatusError) transient() bool {
	return e.Code >= 500 || e.Code == http.StatusTooManyRequests
}

// Get reads the page at u, at most 4 MiB of it, and returns it with the address it came from after the
// redirects. An answer other than 200 OK is a *StatusError. A request that fails for a passing reason (a
// network error, a server error, too many requests) is repeated, see Retries and RetryDelay; when ctx ends,
// the error is ctx's.
func Get(ctx context.Context, client *http.Client, u string) ([]byte, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; hardstyle-playlist-sync)")
	delay := RetryDelay
	for try := 0; ; try++ {
		body, final, err := get(client, req)
		if err == nil {
			return body, final, nil
		}
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		var status *StatusError
		if try >= Retries || (errors.As(err, &status) && !status.transient()) {
			return nil, nil, err
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		delay *= 2
	}
}

// get makes one try of Get.
func get(client *http.Client, req *http.Request) ([]byte, *url.URL, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, &StatusError{URL: req.URL.String(), Code: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, nil, err
	}
	return body, resp.Request.URL, nil
}

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

// FetchPage reads one page of the track list. ErrNoResults means the page is past the last one.
// The list at hardstyle.com/en/tracks is sorted by release date, newest first, 30 tracks per page.
func FetchPage(ctx context.Context, client *http.Client, genre string, page int) ([]Track, error) {
	u := trackListURL(genre, page)
	body, _, err := Get(ctx, client, u)
	if err != nil {
		return nil, err
	}
	tracks, err := parseTrackList(string(body), genre)
	if err != nil && !errors.Is(err, ErrNoResults) {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	return tracks, err
}

// FetchReleases returns the newest tracks of a genre (or of the general list), newest first: all of the first
// pages pages, fewer when the list is shorter.
func FetchReleases(ctx context.Context, client *http.Client, genre string, pages int) ([]Track, error) {
	var out []Track
	for page := 1; page <= pages; page++ {
		if page > 1 {
			select {
			case <-time.After(PageDelay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		tracks, err := FetchPage(ctx, client, genre, page)
		if errors.Is(err, ErrNoResults) {
			break // small genres have fewer pages than requested
		}
		if err != nil {
			return nil, err
		}
		out = append(out, tracks...)
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
		// The length and the label: <div class="duration">…<span class="innerDuration">04:41</span></div> and
		// <div class="label"><a class="link label">I AM HARDSTYLE</a></div>.
		duration, label := 0, ""
		if d := find(n, func(n *html.Node) bool { return isElem(n, "span") && hasClass(n, "innerDuration") }); d != nil {
			duration = parseClock(Clean(text(d)))
		}
		if l := find(n, func(n *html.Node) bool { return isElem(n, "div") && hasClass(n, "label") }); l != nil {
			label = Clean(text(l))
		}
		tracks = append(tracks, Track{ID: attr(n, "data-track-id"), Path: attr(title, "href"), Title: t, Version: v, Artist: a,
			Genre: genre, Label: label, Duration: duration})
	}
	return tracks, nil
}

// parseClock reads a length as the list shows it, "04:41" or "1:02:03", in seconds; 0 when it is no length.
func parseClock(s string) int {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	total := 0
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (i > 0 && n > 59) {
			return 0
		}
		total = total*60 + n
	}
	return total
}

var isoDuration = regexp.MustCompile(`^PT(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)(?:\.\d+)?S)?$`)

// ParseISODuration reads a length in the ISO 8601 form the pages use in their data ("PT4M41S", "PT04M41S",
// "PT1H2M3S") in seconds; 0 when it is no such length.
func ParseISODuration(s string) int {
	m := isoDuration.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(s)))
	if m == nil {
		return 0
	}
	total := 0
	for i, unit := range []int{3600, 60, 1} {
		n, _ := strconv.Atoi(m[i+1]) // a missing part is 0
		total += n * unit
	}
	return total
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
	durationLD    = regexp.MustCompile(`"duration"\s*:\s*"([^"]+)"`)
)

// Details is what the page of a release on hardstyle.com tells besides the list.
type Details struct {
	Date string // release date, yyyy-mm-dd
	ISRC string // international code of the recording; Spotify can search for it. May be empty.
	// Catalog is the catalog number of the release the track is part of: all tracks of an album have the same
	// one ("MINUS288"), whoever the artist. May be empty.
	Catalog string
	// Duration is the length in seconds as the release page names it; 0 when unknown.
	Duration int
	// DateApprox: Date is an estimate (hardtunes.com names only "This week" or "Last week"). The cache does not
	// let an estimate replace a day it knows exactly.
	DateApprox bool
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

	body, _, err := Get(ctx, f.hc, Base+t.Path)
	if err != nil {
		return Details{}, err
	}
	return parseDetails(body)
}

// parseDetails reads the date, the ISRC, the catalog number and the length from the data of a track page
// (JSON-LD).
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
	if l := durationLD.FindSubmatch(page); l != nil {
		d.Duration = ParseISODuration(string(l[1]))
	}
	return d, nil
}
