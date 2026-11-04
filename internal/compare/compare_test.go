package compare

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hardstylesync/internal/config"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/hardtunes"
)

func init() { hardstyle.PageDelay, hardtunes.PageDelay = 0, 0 }

const hardstylePage = `<html><body><div class="listViewWrapper">
<div class="track listView" data-track-id="1"><a class="trackTitle" href="/en/tracks/1/i-dont-know-this-tempo">I Don't Know This Tempo</a><a class="linkTitle" href="/en/tracks/1/i-dont-know-this-tempo">Extended Mix</a><span class="artists">Devin Wild</span></div>
<div class="track listView" data-track-id="2"><a class="trackTitle" href="/en/tracks/2/only-here">Only On Hardstyle</a><span class="artists">Someone</span></div>
</div></body></html>`

const hardtunesPage = `<html><body><div class="row release-list release-list-normal">
<div class="col-xs-6 release-list-item"><div class="release-list-item-info"><div class="release-list-item-info-primary"><p class="release-list-item-title"><a href="https://www.hardtunes.com/tunes/devin-wild-i-dont-know-this-tempo-extended-mix/104444">I Don't Know This Tempo (Extended Mix)</a></p><p class="release-list-item-artist"><a href="https://www.hardtunes.com/artists/devin-wild">Devin Wild</a></p></div><div class="release-list-item-info-secondary"><p class="release-list-item-type"><a href="https://www.hardtunes.com/tunes/devin-wild-i-dont-know-this-tempo-extended-mix/104444">Single tune</a></p><p class="release-list-item-release-date">Today</p></div></div></div>
<div class="col-xs-6 release-list-item"><div class="release-list-item-info"><div class="release-list-item-info-primary"><p class="release-list-item-title"><a href="https://www.hardtunes.com/tunes/somebody-only-on-hardtunes/555">Only On Hardtunes</a></p><p class="release-list-item-artist">Somebody</p></div><div class="release-list-item-info-secondary"><p class="release-list-item-type">Single tune</p><p class="release-list-item-release-date">Today</p></div></div></div>
</div></body></html>`

func TestRunComparesBothSources(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/en/tracks" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, hardstylePage)
	}))
	defer hs.Close()
	ht := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hardstyle/page/1/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, hardtunesPage)
	}))
	defer ht.Close()

	oldHS, oldHT := hardstyle.Base, hardtunes.Base
	hardstyle.Base, hardtunes.Base = hs.URL, ht.URL
	t.Cleanup(func() { hardstyle.Base, hardtunes.Base = oldHS, oldHT })

	cfg := config.Config{Genres: []string{"Hardstyle"}, HardtunesGenres: []string{"hardstyle"}, Pages: 1}
	rep, err := Run(context.Background(), hs.Client(), cfg, func(string, ...any) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Sources) != 2 {
		t.Fatalf("2 sources expected, got %d", len(rep.Sources))
	}
	if len(rep.Both) != 1 || rep.Both[0].Title != "I Don't Know This Tempo" {
		t.Errorf("the shared release (mix suffix ignored) expected in Both, got %+v", rep.Both)
	}
	if only := rep.only(rep.Sources[0]); len(only) != 1 || only[0].Title != "Only On Hardstyle" {
		t.Errorf("only hardstyle.com: %+v", only)
	}
	if only := rep.only(rep.Sources[1]); len(only) != 1 || only[0].Title != "Only On Hardtunes" {
		t.Errorf("only hardtunes.com: %+v", only)
	}

	out := Render(rep)
	for _, want := range []string{"hardstyle.com", "hardtunes.com", "Hardstyle", "Only On Hardstyle", "Only On Hardtunes", "| 2 | 2 | 1 | 1 | 1 |"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

func TestKeyIgnoresMixSuffixAndCase(t *testing.T) {
	a := key(hardstyleTrack("Devin Wild", "I Don't Know This Tempo", "Extended Mix"))
	b := key(hardstyleTrack("devin wild", "I Don't Know This Tempo (Extended Mix)", ""))
	if a != b {
		t.Errorf("same release, different keys: %q vs %q", a, b)
	}
}

func hardstyleTrack(artist, title, version string) hardstyle.Track {
	return hardstyle.Track{Artist: artist, Title: title, Version: version}
}
