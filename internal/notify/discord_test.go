package notify

import (
	"context"
	"encoding/json"
	"errors"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/i18n"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// withLanguage switches the language for one test.
func withLanguage(t *testing.T, name string) {
	t.Helper()
	if err := i18n.SetLanguage(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(i18n.Reset)
}

// webhookServer records the message it receives and answers with 204.
func webhookServer(t *testing.T) (*httptest.Server, *webhook, *int32) {
	t.Helper()
	var got webhook
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s with %q, want POST application/json", r.Method, r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, &got, &calls
}

var day = time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)

func TestRunSummaryInGerman(t *testing.T) {
	srv, got, calls := webhookServer(t)
	s := RunSummary{
		PlaylistID: "abc", At: day, After: 500,
		Added: 2, Removed: 1, Matched: 30, Missing: 2, Deferred: 3, Requests: 9,
		NewTracks: []hardstyle.Track{
			{Artist: "B", Title: "Second"},
			{Artist: "A", Title: "First", Version: "Extended Mix"},
		},
	}
	if err := SendRun(context.Background(), srv.Client(), srv.URL, s); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("%d requests, want one", *calls)
	}
	if len(got.Embeds) != 1 {
		t.Fatalf("message = %+v", got)
	}
	e := got.Embeds[0]
	if e.Title != "Playlist aktualisiert" || e.Color != colorChanged {
		t.Errorf("title=%q color=%#x", e.Title, e.Color)
	}
	if e.URL != "https://open.spotify.com/playlist/abc" || e.Timestamp != "2026-10-29T08:00:00Z" {
		t.Errorf("url=%q timestamp=%q", e.URL, e.Timestamp)
	}
	for _, want := range []string{"**2 neu**, **1 entfernt** · 500 Tracks in der Playlist",
		"• B – Second", "• A – First (Extended Mix)", "[Zur Playlist](https://open.spotify.com/playlist/abc)"} {
		if !strings.Contains(e.Description, want) {
			t.Errorf("description lacks %q:\n%s", want, e.Description)
		}
	}
	if len(e.Fields) != 6 || e.Fields[0].Name != "Neu" || e.Fields[0].Value != "2" || e.Fields[3].Value != "2" || e.Fields[5].Value != "9" {
		t.Errorf("fields = %+v", e.Fields)
	}
	if len(got.AllowedMentions.Parse) != 0 {
		t.Errorf("mentions must not be parsed: %+v", got.AllowedMentions)
	}
}

func TestTheWebhookKeepsItsOwnName(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := SendRun(context.Background(), srv.Client(), srv.URL, RunSummary{At: day}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"username"`) {
		t.Errorf("the message overrides the name of the webhook: %s", body)
	}
}

func TestQuietFullRunHasItsOwnTitle(t *testing.T) {
	withLanguage(t, "en")
	srv, got, _ := webhookServer(t)
	err := SendRun(context.Background(), srv.Client(), srv.URL, RunSummary{PlaylistID: "abc", At: day, After: 498})
	if err != nil {
		t.Fatal(err)
	}
	e := got.Embeds[0]
	if e.Title != "No new tracks" || e.Color != colorQuiet {
		t.Errorf("title=%q color=%#x", e.Title, e.Color)
	}
	if !strings.Contains(e.Description, "No new tracks on hardstyle.com · 498 tracks in the playlist") {
		t.Errorf("description = %q", e.Description)
	}
}

func TestOnlyTheFirstTracksAreListed(t *testing.T) {
	withLanguage(t, "en")
	srv, got, _ := webhookServer(t)
	var news []hardstyle.Track
	for i := 1; i <= 25; i++ {
		news = append(news, hardstyle.Track{Artist: "A", Title: "T"})
	}
	err := SendRun(context.Background(), srv.Client(), srv.URL, RunSummary{At: day, Added: 25, After: 500, NewTracks: news})
	if err != nil {
		t.Fatal(err)
	}
	e := got.Embeds[0]
	if n := strings.Count(e.Description, "• "); n != maxListed {
		t.Errorf("%d tracks listed, want %d:\n%s", n, maxListed, e.Description)
	}
	if !strings.Contains(e.Description, "… and 15 more") {
		t.Errorf("description = %q", e.Description)
	}
	if !strings.HasPrefix(e.Description, "**25 new**, **0 removed** · 500 tracks in the playlist") {
		t.Errorf("description = %q", e.Description)
	}
}

func TestFailureMentionsTheError(t *testing.T) {
	srv, got, _ := webhookServer(t)
	if err := SendFailure(context.Background(), srv.Client(), srv.URL, day, errors.New("Spotify blocks the app")); err != nil {
		t.Fatal(err)
	}
	e := got.Embeds[0]
	if e.Title != "Lauf fehlgeschlagen" || e.Color != colorError || e.Description != "Spotify blocks the app" {
		t.Errorf("embed = %+v", e)
	}
}

func TestServerErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unknown webhook", http.StatusNotFound)
	}))
	defer srv.Close()
	err := SendRun(context.Background(), srv.Client(), srv.URL, RunSummary{At: day})
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "unknown webhook") {
		t.Errorf("err = %v", err)
	}
}

func TestTheWebhookAddressStaysOutOfTheLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL + "/api/webhooks/123/SECRET"
	srv.Close() // the address is gone, the client fails and would name it
	err := SendRun(context.Background(), http.DefaultClient, url, RunSummary{At: day})
	if err == nil {
		t.Fatal("a closed server must fail")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("the token is in the error: %v", err)
	}
	if !strings.Contains(err.Error(), "DISCORD_WEBHOOK_URL") {
		t.Errorf("the error does not name the setting: %v", err)
	}
}

func TestNoWebhookDoesNothing(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&calls, 1) }))
	defer srv.Close()
	if err := SendRun(context.Background(), srv.Client(), "  ", RunSummary{At: day}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("%d requests without a webhook", calls)
	}
}

func TestLongTitlesStayWithinDiscordsLimit(t *testing.T) {
	var news []hardstyle.Track
	for i := 0; i < maxListed; i++ {
		news = append(news, hardstyle.Track{Artist: strings.Repeat("A", 100), Title: strings.Repeat("T", 300)})
	}
	e := runEmbed(RunSummary{At: day, Added: maxListed, After: 1, NewTracks: news})
	if n := len([]rune(e.Description)); n > 4096 {
		t.Errorf("description is %d characters, Discord takes 4096", n)
	}
	if !strings.HasSuffix(e.Description, "…") {
		t.Error("a cut description must end with an ellipsis")
	}
}

func TestMarkdownIsEscaped(t *testing.T) {
	line := trackLine(hardstyle.Track{Artist: "A_B", Title: "*Hit*"})
	if line != `A\_B – \*Hit\*` {
		t.Errorf("trackLine = %q", line)
	}
}
