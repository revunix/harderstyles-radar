package cache

import (
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/spotify"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

func newCache(t *testing.T) (*Cache, string) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return c, path
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

func TestCacheRoundTripAndBlock(t *testing.T) {
	c, path := newCache(t)
	tr := hardstyle.Track{ID: "x", Title: "T", Artist: "A"}
	c.SetDetails(tr, hardstyle.Details{Date: "2026-10-29", ISRC: "NL123"})
	c.SetFound(tr, Found{URIs: []string{"spotify:track:1", "spotify:track:2"}, SpotifyDate: "2026-10-29"})
	c.SetPlaylist(PlaylistSnapshot{ID: "p", SnapshotID: "s1", URIs: []string{"spotify:track:1"}, At: time.Now()})
	c.Block("a", time.Now().Add(time.Hour))
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	c2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := c2.Entry(tr); !ok || e.URI != "spotify:track:1" || len(e.Extra) != 1 || e.ISRC != "NL123" || e.Date != "2026-10-29" || !e.Usable() {
		t.Errorf("entry = %+v %v", e, ok)
	}
	if p := c2.Playlist("p"); p == nil || p.SnapshotID != "s1" || len(p.URIs) != 1 || c2.Playlist("other") != nil {
		t.Errorf("playlist = %+v", p)
	}
	if c2.BlockedUntil("a").IsZero() {
		t.Error("block lost")
	}
	if !c2.BlockedUntil("b").IsZero() {
		t.Error("the block of one app must not hold for another")
	}
	if d := c2.DatesByURI()["spotify:track:2"]; d != "2026-10-29" {
		t.Errorf("dates = %v", c2.DatesByURI())
	}

	var nilCache *Cache
	nilCache.SetFound(tr, Found{URIs: []string{"u"}})
	if _, ok := nilCache.Entry(tr); ok || nilCache.Save() != nil || !nilCache.BlockedUntil("a").IsZero() || nilCache.Playlist("p") != nil {
		t.Error("a nil cache must do nothing")
	}
}

func TestCacheIgnoresAnOldFormat(t *testing.T) {
	c, path := newCache(t)
	c.Save()
	if err := writeFile(path, `{"x":{"uri":"u"}}`); err != nil {
		t.Fatal(err)
	}
	c2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c2.Entry(hardstyle.Track{ID: "x"}); ok {
		t.Error("an old cache file should be ignored, not misread")
	}
}

func TestBetterMatchingLooksAgainForWhatWasMissing(t *testing.T) {
	c, path := newCache(t)
	tr := hardstyle.Track{ID: "a", Title: "Lose Control", Artist: "Juliex"}
	c.SetMissing(tr, spotify.ReasonNotFound, "")
	c.SetFound(hardstyle.Track{ID: "b", Title: "Found", Artist: "A"}, Found{URIs: []string{"spotify:track:B"}, SpotifyDate: "2026-10-29"})
	if e, _ := c.Entry(tr); !e.Usable() {
		t.Fatal("a fresh missing entry holds")
	}
	// A cache of an older matcher: the missing entry must not hold any more, the found one stays.
	c.f.Matcher = 1
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c2, _ := Load(path)
	if e, ok := c2.Entry(tr); !ok || e.Usable() || e.Tries != 1 {
		t.Errorf("the missing entry must be looked up again: %+v", e)
	}
	if e, _ := c2.Entry(hardstyle.Track{ID: "b"}); !e.Usable() || e.URI != "spotify:track:B" {
		t.Errorf("a found entry stays: %+v", e)
	}
	if c2.f.Matcher != matcherVersion {
		t.Errorf("matcher = %d", c2.f.Matcher)
	}
}

func TestARefusedTrackIsNeverTakenAgain(t *testing.T) {
	c, path := newCache(t)
	tr := hardstyle.Track{ID: "x", Title: "T", Artist: "A"}
	c.SetFound(tr, Found{URIs: []string{"spotify:track:1", "spotify:track:2"}, SpotifyDate: "2026-10-29"})

	c.Reject(tr, "spotify:track:1")
	e, _ := c.Entry(tr)
	if e.URI != "" || len(e.Extra) != 0 || !e.At.IsZero() || !slices.Equal(e.Rejected, []string{"spotify:track:1"}) {
		t.Fatalf("after reject: %+v", e)
	}
	// Refusing the same track twice remembers it once.
	c.Reject(tr, "spotify:track:1")
	if e, _ = c.Entry(tr); len(e.Rejected) != 1 {
		t.Errorf("rejected twice: %v", e.Rejected)
	}

	// Looking it up again must not bring the refused track back, not even as a remix next to another match.
	c.SetFound(tr, Found{URIs: []string{"spotify:track:1", "spotify:track:3"}, SpotifyDate: "2026-10-29"})
	if e, _ = c.Entry(tr); e.URI != "spotify:track:3" || len(e.Extra) != 0 {
		t.Errorf("the refused track came back: %+v", e)
	}

	c.Forget(tr)
	if e, _ = c.Entry(tr); e.URI != "" || !e.At.IsZero() || len(e.Rejected) != 1 {
		t.Errorf("forget drops the match but keeps the refusal: %+v", e)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c2, _ := Load(path)
	if e, ok := c2.Entry(tr); !ok || !slices.Equal(e.Rejected, []string{"spotify:track:1"}) {
		t.Errorf("the refusal must survive a reload: %+v %v", e, ok)
	}
}

func TestLegacyBlockMovesToTheFirstApp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if err := writeFile(path, `{"version":`+strconv.Itoa(cacheVersion)+`,"matcher":`+strconv.Itoa(matcherVersion)+`,"tracks":{},"blocked_until":"`+until+`"}`); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c.AdoptLegacyBlock("a")
	if c.BlockedUntil("a").IsZero() || !c.BlockedUntil("b").IsZero() {
		t.Errorf("a = %v, b = %v", c.BlockedUntil("a"), c.BlockedUntil("b"))
	}
}
