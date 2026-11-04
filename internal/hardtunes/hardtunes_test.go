package hardtunes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"hardstylesync/internal/hardstyle"
)

func init() { PageDelay, hardstyle.RetryDelay = 0, 0 }

// fixture is one page of a genre list as hardtunes.com renders it: one tune with four artists and one
// album ("2 Tunes"). The class attribute of an item has line breaks, like on the real site.
const fixture = `<html><body><div class="panel-body"><div class="row release-list release-list-normal "><div class="
col-xs-6
release-list-item
"><div class="release-list-item-artwork"><a href="https://www.hardtunes.com/tunes/antergy-beat-conductors-essive-and-testarossa-finish-line-extended-mix/104531"><img data-src="https://content.hardtunes.com/products/104531/109506/248x248.jpg" alt="" class="lazy img-responsive"></a></div><div class="release-list-item-info"><div class="release-list-item-info-primary"><p class="release-list-item-title"><a href="https://www.hardtunes.com/tunes/antergy-beat-conductors-essive-and-testarossa-finish-line-extended-mix/104531">Finish Line (Extended Mix)</a></p><p class="release-list-item-artist"><a href="https://www.hardtunes.com/artists/antergy">Antergy</a>,&nbsp;<a href="https://www.hardtunes.com/artists/beat-conductors">Beat&nbsp;Conductors</a>,&nbsp;<a href="https://www.hardtunes.com/artists/essive">Essive</a>&nbsp;and&nbsp;<a href="https://www.hardtunes.com/artists/testarossa">Testarossa</a></p><p class="release-list-item-label"><a href="https://www.hardtunes.com/labels/defqon.1-records">Defqon.1 Records</a></p></div><div class="release-list-item-info-secondary"><p class="release-list-item-type"><a href="https://www.hardtunes.com/tunes/antergy-beat-conductors-essive-and-testarossa-finish-line-extended-mix/104531">Single tune</a></p><p class="release-list-item-release-date">Today</p><p class="release-list-item-price">&euro; 1,49</p></div></div></div><div class="col-xs-6 release-list-item"><div class="release-list-item-artwork"><a href="https://www.hardtunes.com/albums/aetherion-wild-ones/25617"><img data-src="https://content.hardtunes.com/albums/25617/27003/248x248.jpg" alt="" class="lazy img-responsive"></a></div><div class="release-list-item-info"><div class="release-list-item-info-primary"><p class="release-list-item-title"><a href="https://www.hardtunes.com/albums/aetherion-wild-ones/25617">Wild Ones</a></p><p class="release-list-item-artist"><a href="https://www.hardtunes.com/artists/aetherion">Aetherion</a></p><p class="release-list-item-label"><a href="https://www.hardtunes.com/labels/we-are-mood">We Are Mood</a></p></div><div class="release-list-item-info-secondary"><p class="release-list-item-type"><a href="https://www.hardtunes.com/albums/aetherion-wild-ones/25617">2 Tunes</a></p><p class="release-list-item-release-date">23.10.2026</p><p class="release-list-item-price">&euro; 0,99</p></div></div></div></div></div>
<div class="panel-body"><div class="top-10-list-item"><p class="top-10-list-item-title"><a href="https://www.hardtunes.com/tunes/some-top-ten-tune/1">Top Ten Decoy</a></p></div></div></body></html>`

func TestParseList(t *testing.T) {
	tracks, err := parseList(fixture, "hardstyle")
	if err != nil {
		t.Fatalf("parseList: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("2 releases expected (the top 10 list is not a release), got %d", len(tracks))
	}
	first := tracks[0]
	if first.ID != "104531" || first.Title != "Finish Line (Extended Mix)" ||
		first.Artist != "Antergy, Beat Conductors, Essive and Testarossa" ||
		first.Path != "https://www.hardtunes.com/tunes/antergy-beat-conductors-essive-and-testarossa-finish-line-extended-mix/104531" ||
		first.Genre != "Hardstyle" || first.Label != "Defqon.1 Records" || first.Type != "Single tune" ||
		first.DateText != "Today" || first.Date != time.Now().Format("2006-01-02") || !first.DateExact || first.IsAlbum() {
		t.Errorf("first release parsed wrong: %+v", first)
	}
	second := tracks[1]
	if second.ID != "25617" || second.Title != "Wild Ones" || second.Artist != "Aetherion" || second.Genre != "Hardstyle" ||
		second.Type != "2 Tunes" || second.DateText != "23.10.2026" || second.Date != "2026-10-23" || !second.DateExact || !second.IsAlbum() {
		t.Errorf("album parsed wrong: %+v", second)
	}
}

func TestParseListSiteChanged(t *testing.T) {
	if _, err := parseList(`<html><body><p>nothing here</p></body></html>`, "hardstyle"); err == nil || errors.Is(err, ErrNoResults) {
		t.Errorf("a page without the list must be an error, got %v", err)
	}
}

func TestParseListEmpty(t *testing.T) {
	if _, err := parseList(`<html><body><div class="row release-list release-list-normal "></div></body></html>`, "hardstyle"); !errors.Is(err, ErrNoResults) {
		t.Errorf("an empty list is past the last page, got %v", err)
	}
}

func TestTrackListURL(t *testing.T) {
	if u := trackListURL("hard-techno", 3); u != Base+"/hard-techno/page/3/" {
		t.Errorf("trackListURL = %q", u)
	}
}

// listSite is a small hardtunes.com: two pages of a genre, every page past them redirects to the last page
// like the real site does, and it counts the requests.
type listSite struct {
	requests atomic.Int32
}

func (s *listSite) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	page := func(id, title string) string {
		return fmt.Sprintf(`<html><body><div class="row release-list release-list-normal"><div class="col-xs-6 release-list-item"><div class="release-list-item-info"><div class="release-list-item-info-primary"><p class="release-list-item-title"><a href="https://www.hardtunes.com/tunes/x/%s">%s</a></p><p class="release-list-item-artist">Some Artist</p></div><div class="release-list-item-info-secondary"><p class="release-list-item-type">Single tune</p><p class="release-list-item-release-date">Today</p></div></div></div></div></body></html>`, id, title)
	}
	mux.HandleFunc("/hardstyle/page/1/", func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		fmt.Fprint(w, page("111", "First Page Tune"))
	})
	mux.HandleFunc("/hardstyle/page/2/", func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		fmt.Fprint(w, page("222", "Second Page Tune"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		http.Redirect(w, r, "/hardstyle/page/2/", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := Base
	Base = srv.URL
	t.Cleanup(func() { Base = old })
	return srv
}

func TestFetchReleasesStopsPastTheLastPage(t *testing.T) {
	site := &listSite{}
	srv := site.start(t)
	tracks, err := FetchReleases(context.Background(), srv.Client(), "hardstyle", 10)
	if err != nil {
		t.Fatalf("FetchReleases: %v", err)
	}
	if len(tracks) != 2 || tracks[0].ID != "111" || tracks[1].ID != "222" {
		t.Errorf("the two existing pages expected, got %+v", tracks)
	}
	if site.requests.Load() != 4 {
		t.Errorf("two pages, the one past the end and its redirect: want 4 requests, %d were made", site.requests.Load())
	}
}

func TestFetchPageRetriesServerErrors(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `<div class="row release-list"><div class="release-list-item"><div class="release-list-item-info"><div class="release-list-item-info-primary"><p class="release-list-item-title"><a href="https://www.hardtunes.com/tunes/x/7">T</a></p><p class="release-list-item-artist">A</p></div><div class="release-list-item-info-secondary"><p class="release-list-item-release-date">Today</p></div></div></div></div>`)
	}))
	defer srv.Close()
	old := Base
	Base = srv.URL
	defer func() { Base = old }()
	tracks, err := FetchPage(context.Background(), srv.Client(), "hardstyle", 1)
	if err != nil || len(tracks) != 1 || tracks[0].ID != "7" {
		t.Errorf("got %+v %v, want the page after the server errors", tracks, err)
	}
	if requests.Load() != 3 {
		t.Errorf("%d requests, want 3", requests.Load())
	}
}

func TestDetailFetcherDoesNotRepeatNotFound(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	_, err := NewDetailFetcher(srv.Client(), 0).Fetch(context.Background(), Track{Track: hardstyle.Track{Path: srv.URL + "/tunes/x/1"}})
	var status *hardstyle.StatusError
	if !errors.As(err, &status) || status.Code != http.StatusNotFound {
		t.Errorf("got %v, want a 404 StatusError", err)
	}
	if requests.Load() != 1 {
		t.Errorf("%d requests, want 1", requests.Load())
	}
}

// Run with HT_LIVE=1 to check the parser against the real site.
func TestLiveSite(t *testing.T) {
	if os.Getenv("HT_LIVE") == "" {
		t.Skip("HT_LIVE=1 to test against hardtunes.com")
	}
	tracks, err := FetchReleases(context.Background(), http.DefaultClient, "hardstyle", 1)
	if err != nil {
		t.Fatalf("live site: %v", err)
	}
	if len(tracks) == 0 {
		t.Fatal("live site: no releases on page 1")
	}
	t.Logf("%d releases, the newest: %s - %s (%s)", len(tracks), tracks[0].Artist, tracks[0].Title, tracks[0].Date)
	exact, labeled := 0, 0
	for _, tr := range tracks {
		if tr.ID == "" || tr.Title == "" || tr.Artist == "" {
			t.Errorf("incomplete release: %+v", tr)
		}
		if tr.DateExact {
			exact++
		}
		if tr.Release().Label != "" {
			labeled++
		}
	}
	t.Logf("%d of %d releases with an exact day, %d with a label", exact, len(tracks), labeled)
	f := NewDetailFetcher(http.DefaultClient, time.Second)
	tunes, withDuration := 0, 0
	for _, tr := range tracks {
		if tr.IsAlbum() || tunes == 10 {
			continue
		}
		tunes++
		d, err := f.Fetch(context.Background(), tr)
		if err != nil {
			t.Fatalf("details of %q: %v", tr.Title, err)
		}
		if d.Duration > 0 {
			withDuration++
		}
		t.Logf("details of %q (%s): %+v", tr.Title, tr.DateText, d)
	}
	t.Logf("tune pages: %d of %d with a duration", withDuration, tunes)
}

func TestParseDate(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		text, want string
		exact      bool
	}{
		{"Today", "2026-11-04", true},
		{"Yesterday", "2026-11-03", true},
		{"This week", "2026-10-31", false},
		{"Last week", "2026-10-27", false},
		{"23.10.2026", "2026-10-23", true},
		{"", "", false},
		{"soon", "", false},
	} {
		if got, exact := parseDate(c.text, now); got != c.want || exact != c.exact {
			t.Errorf("parseDate(%q) = %q %v, want %q %v", c.text, got, exact, c.want, c.exact)
		}
	}
}

func TestReleaseSplitsTheVersion(t *testing.T) {
	for title, want := range map[string][2]string{
		"Finish Line (Extended Mix)":                       {"Finish Line", "Extended Mix"},
		"Danse Macabre (Rave Till The End) (Extended Mix)": {"Danse Macabre (Rave Till The End)", "Extended Mix"},
		"Wild Ones":  {"Wild Ones", ""},
		"(Untitled)": {"(Untitled)", ""},
	} {
		r := Track{Track: hardstyle.Track{Title: title}}.Release()
		if r.Title != want[0] || r.Version != want[1] {
			t.Errorf("%q -> title %q version %q, want %q %q", title, r.Title, r.Version, want[0], want[1])
		}
	}
}

func TestReleaseCarriesTheLabel(t *testing.T) {
	r := Track{Track: hardstyle.Track{Title: "Wild Ones (Extended)"}, Label: "We Are Mood"}.Release()
	if r.Label != "We Are Mood" || r.Title != "Wild Ones" {
		t.Errorf("release = %+v", r)
	}
}

func TestParseDetailsReadsTheISRCMetaAndKeepsTheListDate(t *testing.T) {
	page := []byte(`<meta itemprop="genre" content="Hardstyle"><meta itemprop="isrcCode" content="nls-762600404"><meta itemprop="duration" content="PT4M6S"><meta itemprop="releaseDate" content="Last week">`)
	d := parseDetails(Track{Date: "2026-10-27"}, page)
	if d.ISRC != "NLS762600404" || d.Date != "2026-10-27" || d.Duration != 246 || !d.DateApprox {
		t.Errorf("details = %+v", d)
	}
	d = parseDetails(Track{Date: "2026-10-23", DateExact: true}, []byte(`<meta itemprop="duration" content="PT1H0M5S">`))
	if d.Date != "2026-10-23" || d.DateApprox || d.Duration != 3605 {
		t.Errorf("an exact day of the list stays exact: %+v", d)
	}
	if d := parseDetails(Track{}, []byte(`<meta itemprop="duration" content="soon">`)); d.Duration != 0 {
		t.Errorf("an unreadable length is 0: %+v", d)
	}
}

func TestParseDetailsTakesTheDayAndSecondsOfThePage(t *testing.T) {
	for _, c := range []struct {
		name, page   string
		list         Track
		wantDate     string
		wantApprox   bool
		wantDuration int
	}{
		{"the page names the day", `<meta property="music:release_date" content="2026-10-30"/><meta property="music:duration" content="212"/><meta itemprop="duration" content="PT3M32S">`,
			Track{Date: "2026-10-27"}, "2026-10-30", false, 212},
		{"seconds when the itemprop is missing", `<meta property="music:duration" content="212"/>`,
			Track{Date: "2026-10-27"}, "2026-10-27", true, 212},
		{"an invalid day keeps the list's estimate", `<meta property="music:release_date" content="Last week"/>`,
			Track{Date: "2026-10-27"}, "2026-10-27", true, 0},
		{"an invalid day keeps the list's exact day", `<meta property="music:release_date" content="2026-13-40"/>`,
			Track{Date: "2026-10-23", DateExact: true}, "2026-10-23", false, 0},
	} {
		d := parseDetails(c.list, []byte(c.page))
		if d.Date != c.wantDate || d.DateApprox != c.wantApprox || d.Duration != c.wantDuration {
			t.Errorf("%s: %+v, want %s approx=%v %ds", c.name, d, c.wantDate, c.wantApprox, c.wantDuration)
		}
	}
}

func TestExpandAlbumsTakesTheTracklistOnceAndOnlyIt(t *testing.T) {
	var reads atomic.Int32
	var base string
	item := func(path, title string) string {
		return fmt.Sprintf(`<div class="col-xs-6 release-list-item"><div class="release-list-item-info"><div class="release-list-item-info-primary"><p class="release-list-item-title"><a href="%s%s">%s</a></p><p class="release-list-item-artist">A</p></div><div class="release-list-item-info-secondary"><p class="release-list-item-type">Single tune</p><p class="release-list-item-release-date">23.10.2026</p></div></div></div>`, base, path, title)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		fmt.Fprint(w, `<div class="panel"><div class="panel-heading">Tracklist</div>`+item("/tunes/a/1", "A1")+item("/tunes/b/2", "B2")+`</div>`+
			`<div class="panel"><div class="panel-heading">People who bought this also bought</div>`+item("/tunes/x/9", "X9")+`</div>`)
	}))
	defer srv.Close()
	base = srv.URL
	list := []Track{
		{Track: hardstyle.Track{ID: "1", Title: "A1", Path: base + "/tunes/a/1"}, Type: "Single tune"},
		{Track: hardstyle.Track{ID: "50", Title: "Album", Path: base + "/albums/album/50", Genre: "Terror"}, Type: "2 Tunes"},
	}
	for range 2 {
		got, err := ExpandAlbums(context.Background(), srv.Client(), list)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, tr := range got {
			ids = append(ids, tr.ID)
		}
		if !slices.Equal(ids, []string{"1", "2"}) || got[1].Genre != "Terror" || got[1].Date != "2026-10-23" {
			t.Fatalf("expanded = %+v, want the single and the album's other tune, in the album's genre", got)
		}
	}
	if reads.Load() != 1 {
		t.Errorf("the album page was read %d times, want once", reads.Load())
	}
}
