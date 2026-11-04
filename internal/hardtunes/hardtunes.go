// Package hardtunes reads the release lists of hardtunes.com, the second source besides hardstyle.com:
// hardstyle.com does not always have everything, hardtunes.com does.
package hardtunes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/i18n"
)

// Track is one entry of a hardtunes.com genre list: a tune ("Single tune") or an album ("2 Tunes"), whose
// tunes are listed on the album page itself.
type Track struct {
	hardstyle.Track
	Label    string // record label, may be empty
	Type     string // "Single tune", "2 Tunes", ...
	Date     string // the day of the release, yyyy-mm-dd; empty when the site names no day
	DateText string // as the list shows it: "Today", "Last week", "23.10.2026"
	// DateExact: the list names the day ("23.10.2026", "Today", "Yesterday"), not only a week, so Date is the
	// real day, not an estimate.
	DateExact bool
}

// ErrNoResults means the page is past the last page of the genre (or the genre is empty).
var ErrNoResults = errors.New("no results")

// Base is the address of hardtunes.com; tests point it at a local server.
var Base = "https://www.hardtunes.com"

// PageDelay is the pause between two requests to hardtunes.com, to be friendly to the site. Tests shorten it.
var PageDelay = time.Second

// DetailDelay is the pause between two detail-page requests to hardtunes.com. Tests shorten it.
var DetailDelay = time.Second

// Genres are the genre lists of hardtunes.com that are registered as a source. A genre list lives at
// https://www.hardtunes.com/<genre>/page/<page>/
var Genres = []string{"hardstyle", "hardcore", "terror", "frenchcore", "early-rave", "hard-techno"}

// labels name a genre path for the screen and the log.
var labels = map[string]string{
	"hardstyle":   "Hardstyle",
	"hardcore":    "Hardcore",
	"terror":      "Terror",
	"frenchcore":  "Frenchcore",
	"early-rave":  "Early Rave",
	"hard-techno": "Hard Techno",
}

// GenreLabel names a genre path for the screen and the log.
func GenreLabel(genre string) string {
	if l, ok := labels[genre]; ok {
		return l
	}
	return genre
}

// trackListURL is the address of one page of a genre list: /<genre>/page/<page>/
func trackListURL(genre string, page int) string {
	return fmt.Sprintf("%s/%s/page/%d/", Base, genre, page)
}

// FetchPage reads one page of a genre list, newest first, 20 releases per page. ErrNoResults means the page is
// past the last one: hardtunes.com answers those with a redirect to the last page, which the client follows,
// so a different address in the answer means the requested page does not exist. A request that fails for a
// passing reason is repeated (hardstyle.Retries).
func FetchPage(ctx context.Context, client *http.Client, genre string, page int) ([]Track, error) {
	u := trackListURL(genre, page)
	body, final, err := hardstyle.Get(ctx, client, u)
	if err != nil {
		return nil, err
	}
	if p, err := url.Parse(u); err == nil && final.Path != p.Path {
		return nil, ErrNoResults // redirected to another page: past the last one
	}
	tracks, err := parseList(string(body), genre)
	if err != nil && !errors.Is(err, ErrNoResults) {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	return tracks, err
}

// FetchReleases returns the newest releases of a genre, newest first: all of the first pages pages, fewer
// when the list is shorter.
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

var productID = regexp.MustCompile(`/(\d+)/?$`)

// parseList reads one page of a genre list: every div.release-list-item is one release. The page also shows
// a top 10 list with its own classes, only the release-list items are read.
func parseList(page, genre string) ([]Track, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil, err
	}
	list := find(doc, func(n *html.Node) bool { return isElem(n, "div") && hasClass(n, "release-list") })
	if list == nil {
		return nil, fmt.Errorf("%s", i18n.T("keine Release-Liste gefunden, hat sich die Seite wieder geändert?", "no release list found, did the site change again?"))
	}
	tracks := parseItems(findAll(list, func(n *html.Node) bool { return hasClass(n, "release-list-item") }), genre)
	if len(tracks) == 0 {
		return nil, ErrNoResults
	}
	return tracks, nil
}

// parseItems reads the release-list items of a page or of a panel.
func parseItems(items []*html.Node, genre string) []Track {
	var tracks []Track
	for _, n := range items {
		t, ok := parseItem(n, genre)
		if ok {
			tracks = append(tracks, t)
		}
	}
	return tracks
}

// parseItem reads one release-list item: the title, the artists, the label, the kind and the date.
func parseItem(n *html.Node, genre string) (Track, bool) {
	info := find(n, func(n *html.Node) bool { return isElem(n, "div") && hasClass(n, "release-list-item-info") })
	primary := find(n, func(n *html.Node) bool { return isElem(n, "div") && hasClass(n, "release-list-item-info-primary") })
	secondary := find(n, func(n *html.Node) bool { return isElem(n, "div") && hasClass(n, "release-list-item-info-secondary") })
	if info == nil || primary == nil || secondary == nil {
		return Track{}, false
	}
	title := find(primary, func(n *html.Node) bool { return isElem(n, "p") && hasClass(n, "release-list-item-title") })
	artists := find(primary, func(n *html.Node) bool { return isElem(n, "p") && hasClass(n, "release-list-item-artist") })
	if title == nil || artists == nil {
		return Track{}, false
	}
	// Not every artist is a link, so take the whole text; &nbsp; between the names becomes a space.
	link := find(title, func(n *html.Node) bool { return isElem(n, "a") })
	a := hardstyle.Clean(strings.ReplaceAll(text(artists), string(rune(0xA0)), " ")) // &nbsp; between the names
	t := hardstyle.Clean(text(link))
	if t == "" || a == "" {
		return Track{}, false
	}
	id := ""
	if m := productID.FindStringSubmatch(attr(link, "href")); m != nil {
		id = m[1]
	}
	label := find(primary, func(n *html.Node) bool { return isElem(n, "p") && hasClass(n, "release-list-item-label") })
	typ := find(secondary, func(n *html.Node) bool { return isElem(n, "p") && hasClass(n, "release-list-item-type") })
	date := find(secondary, func(n *html.Node) bool { return isElem(n, "p") && hasClass(n, "release-list-item-release-date") })
	dateText := hardstyle.Clean(text(date))
	day, exact := parseDate(dateText, time.Now())
	return Track{
		Track:     hardstyle.Track{ID: id, Path: attr(link, "href"), Title: t, Artist: a, Genre: GenreLabel(genre)},
		Label:     hardstyle.Clean(text(label)),
		Type:      hardstyle.Clean(text(typ)),
		Date:      day,
		DateText:  dateText,
		DateExact: exact,
	}, true
}

// parseDate reads the day of the release as the list shows it. The fresh releases have no day but a word
// ("Today", "Last week"), the older ones a day ("23.10.2026"). A week becomes its middle, so the date is near
// enough for the comparison with the date on Spotify; exact tells whether the list named the day itself
// ("23.10.2026", "Today", "Yesterday") rather than only a week.
func parseDate(text string, now time.Time) (date string, exact bool) {
	switch strings.ToLower(text) {
	case "today":
		return now.Format("2006-01-02"), true
	case "yesterday":
		return now.AddDate(0, 0, -1).Format("2006-01-02"), true
	case "this week":
		return now.AddDate(0, 0, -4).Format("2006-01-02"), false
	case "last week":
		return now.AddDate(0, 0, -8).Format("2006-01-02"), false
	}
	if t, err := time.Parse("02.01.2006", text); err == nil {
		return t.Format("2006-01-02"), true
	}
	return "", false
}

// IsAlbum reports whether the release is an album ("2 Tunes"), not a single tune.
func (t Track) IsAlbum() bool {
	return t.Type != "" && !strings.EqualFold(t.Type, "Single tune")
}

// Release returns the release as a plain hardstyle.Track, with a trailing "(Extended Mix)" of the title split
// into Version, the way hardstyle.com lists it, so the two sources can be deduplicated and matched alike. The
// label goes along.
func (t Track) Release() hardstyle.Track {
	tr := t.Track
	tr.Label = t.Label
	if i := strings.LastIndex(tr.Title, "("); i > 0 && strings.HasSuffix(tr.Title, ")") {
		if v := hardstyle.Clean(tr.Title[i+1 : len(tr.Title)-1]); v != "" {
			tr.Version, tr.Title = v, hardstyle.Clean(tr.Title[:i])
		}
	}
	return tr
}

// ExpandAlbums replaces the albums of a list by their tunes, at the place of the album, so the list stays newest
// first: the album page has the tracklist, and only the tunes are releases the playlist can hold. The tunes
// that the list already names as singles are left out.
func ExpandAlbums(ctx context.Context, client *http.Client, tracks []Track) ([]Track, error) {
	if !slices.ContainsFunc(tracks, Track.IsAlbum) {
		return tracks, nil
	}
	seen := map[string]bool{}
	for _, t := range tracks {
		if !t.IsAlbum() {
			seen[t.ID] = true
		}
	}
	var out []Track
	var errs []error
	for _, t := range tracks {
		if !t.IsAlbum() {
			out = append(out, t)
			continue
		}
		tunes, err := albumTunes(ctx, client, t)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Path, err))
			continue
		}
		for _, tune := range tunes {
			if !seen[tune.ID] {
				seen[tune.ID] = true
				out = append(out, tune)
			}
		}
	}
	return out, errors.Join(errs...)
}

// albumMemo keeps the tracklists read in this process: a tracklist never changes, and the service checks the
// lists every few minutes, so an album page is read once per process.
var (
	albumMu   sync.Mutex
	albumMemo = map[string][]Track{}
)

// albumTunes reads the tracklist of an album page.
func albumTunes(ctx context.Context, client *http.Client, album Track) ([]Track, error) {
	albumMu.Lock()
	memo, ok := albumMemo[album.Path]
	albumMu.Unlock()
	if ok {
		return memo, nil
	}
	body, _, err := hardstyle.Get(ctx, client, album.Path)
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	var out []Track
	for _, panel := range findAll(doc, func(n *html.Node) bool { return isElem(n, "div") && hasClass(n, "panel") }) {
		head := find(panel, func(n *html.Node) bool { return isElem(n, "div") && hasClass(n, "panel-heading") })
		if head == nil || strings.TrimSpace(text(head)) != "Tracklist" {
			continue
		}
		items := findAll(panel, func(n *html.Node) bool { return hasClass(n, "release-list-item") })
		out = append(out, parseItems(items, album.Genre)...)
	}
	if len(out) == 0 {
		return nil, errors.New(i18n.T("keine Trackliste auf der Albumseite gefunden", "no tracklist found on the album page"))
	}
	albumMu.Lock()
	albumMemo[album.Path] = out
	albumMu.Unlock()
	return out, nil
}

// The detail pages carry the ISRC and the length as meta tags, not as JSON-LD like hardstyle.com. Their
// itemprop releaseDate is only the word of the list ("Last week"), but the Open Graph tags name the day itself
// and the length in seconds.
var (
	isrcMeta        = regexp.MustCompile(`itemprop="isrcCode"\s+content="([A-Za-z0-9-]+)"`)
	durationMeta    = regexp.MustCompile(`itemprop="duration"\s+content="([^"]*)"`)
	releaseDateMeta = regexp.MustCompile(`property="music:release_date"\s+content="([^"]*)"`)
	secondsMeta     = regexp.MustCompile(`property="music:duration"\s+content="(\d+)"`)
)

// DetailFetcher reads the ISRC, the length and the day of a release from its page on hardtunes.com. The address
// of the page is absolute (Track.Path). They never change, so the cache keeps them; the requests are spaced out.
type DetailFetcher struct {
	hc    *http.Client
	delay time.Duration
	mu    sync.Mutex
	next  time.Time
}

func NewDetailFetcher(hc *http.Client, delay time.Duration) *DetailFetcher {
	return &DetailFetcher{hc: hc, delay: delay}
}

// Fetch reads the page of the release.
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
	return parseDetails(t, body), nil
}

// parseDetails reads the ISRC, the length and the day of the release from the meta tags of a tune page. A page
// without a valid day keeps the day of the list, which is an estimate when the list named only the week.
func parseDetails(t Track, page []byte) hardstyle.Details {
	d := hardstyle.Details{Date: t.Date, DateApprox: !t.DateExact}
	if m := releaseDateMeta.FindSubmatch(page); m != nil {
		if day, err := time.Parse("2006-01-02", string(m[1])); err == nil {
			d.Date, d.DateApprox = day.Format("2006-01-02"), false
		}
	}
	if m := isrcMeta.FindSubmatch(page); m != nil {
		d.ISRC = strings.ToUpper(strings.ReplaceAll(string(m[1]), "-", ""))
	}
	if m := durationMeta.FindSubmatch(page); m != nil {
		d.Duration = hardstyle.ParseISODuration(string(m[1]))
	}
	if m := secondsMeta.FindSubmatch(page); m != nil && d.Duration == 0 {
		d.Duration, _ = strconv.Atoi(string(m[1]))
	}
	return d
}

func isElem(n *html.Node, tag string) bool { return n.Type == html.ElementNode && n.Data == tag }

func attr(n *html.Node, key string) string {
	if n == nil {
		return ""
	}
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
	if n == nil {
		return nil
	}
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
	if n == nil {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(text(c))
	}
	return sb.String()
}
