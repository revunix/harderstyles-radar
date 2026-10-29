package sync

import (
	"context"
	"encoding/json"
	"hardstylesync/internal/config"
	"hardstylesync/internal/hardstyle"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// discordMessage is the part of the webhook body these tests look at.
type discordMessage struct {
	Embeds []struct {
		Title       string
		Description string
		Fields      []struct{ Name, Value string }
	}
}

// discordServer collects the messages of a webhook.
type discordServer struct {
	mu   sync.Mutex
	msgs []discordMessage
}

func (d *discordServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m discordMessage
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Errorf("body: %v", err)
		}
		d.mu.Lock()
		d.msgs = append(d.msgs, m)
		d.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (d *discordServer) messages() []discordMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]discordMessage(nil), d.msgs...)
}

func TestTheRunSummaryGoesToDiscord(t *testing.T) {
	site := &fakeSite{tracks: siteTracks(3)}
	srv := site.start(t)
	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	sp.SetDelay(0)
	_, path := newCache(t)

	d := &discordServer{}
	hook := d.start(t)
	cfg := config.Config{
		PlaylistID: "p", Genres: []string{hardstyle.AnyGenre}, Pages: 5, MaxTracks: 500,
		MaxSearches: 100, DateTolerance: 14, CacheFile: path, Description: descriptionOff,
		DiscordWebhook: hook.URL,
	}

	// A full run reports, even when it changed something.
	if _, err := Run(context.Background(), srv.Client(), sp, cfg, false, false, nil); err != nil {
		t.Fatal(err)
	}
	msgs := d.messages()
	if len(msgs) != 1 {
		t.Fatalf("%d messages after the full run, want 1", len(msgs))
	}
	e := msgs[0].Embeds[0]
	if e.Title != "Playlist aktualisiert" || !strings.Contains(e.Description, "• A – T1") {
		t.Errorf("summary = %+v", e)
	}
	if len(e.Fields) != 6 || e.Fields[0].Value != "3" {
		t.Errorf("fields = %+v", e.Fields)
	}

	// A check that finds nothing new stays quiet.
	if _, err := Run(context.Background(), srv.Client(), sp, cfg, false, true, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(d.messages()); n != 1 {
		t.Errorf("%d messages after a quiet check, want still 1", n)
	}

	// A check with new tracks reports them.
	site.mu.Lock()
	site.tracks = append([]hardstyle.Track{{ID: "n2", Title: "N2", Artist: "A"}, {ID: "n1", Title: "N1", Artist: "A"}}, site.tracks...)
	site.mu.Unlock()
	if _, err := Run(context.Background(), srv.Client(), sp, cfg, false, true, nil); err != nil {
		t.Fatal(err)
	}
	msgs = d.messages()
	if len(msgs) != 2 {
		t.Fatalf("%d messages after a check with news, want 2", len(msgs))
	}
	e = msgs[1].Embeds[0]
	for _, want := range []string{"**2 neu**", "• A – N2", "• A – N1"} {
		if !strings.Contains(e.Description, want) {
			t.Errorf("description lacks %q:\n%s", want, e.Description)
		}
	}

	// A dry run sends nothing.
	if _, err := Run(context.Background(), srv.Client(), sp, cfg, true, false, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(d.messages()); n != 2 {
		t.Errorf("%d messages after a dry run, want still 2", n)
	}
}

func TestAFailedRunIsReportedToDiscord(t *testing.T) {
	empty := &fakeSite{} // no releases at all: the run fails
	srv := empty.start(t)
	sp := newFakeClient(t, &fakeSpotify{})
	_, path := newCache(t)

	d := &discordServer{}
	hook := d.start(t)
	cfg := config.Config{
		PlaylistID: "p", Genres: []string{hardstyle.AnyGenre}, Pages: 1, MaxTracks: 500,
		MaxSearches: 1, CacheFile: path, Description: descriptionOff, DiscordWebhook: hook.URL,
	}
	if _, err := Run(context.Background(), srv.Client(), sp, cfg, false, false, nil); err == nil {
		t.Fatal("the run must fail")
	}
	msgs := d.messages()
	if len(msgs) != 1 {
		t.Fatalf("%d messages, want 1", len(msgs))
	}
	if e := msgs[0].Embeds[0]; e.Title != "Lauf fehlgeschlagen" || !strings.Contains(e.Description, "keine Releases") {
		t.Errorf("failure summary = %+v", e)
	}
}

func TestACancelledRunSaysNothing(t *testing.T) {
	empty := &fakeSite{}
	srv := empty.start(t)
	sp := newFakeClient(t, &fakeSpotify{})
	_, path := newCache(t)

	d := &discordServer{}
	hook := d.start(t)
	cfg := config.Config{
		PlaylistID: "p", Genres: []string{hardstyle.AnyGenre}, Pages: 1, MaxTracks: 500,
		MaxSearches: 1, CacheFile: path, Description: descriptionOff, DiscordWebhook: hook.URL,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, srv.Client(), sp, cfg, false, false, nil); err == nil {
		t.Fatal("the run must fail")
	}
	if n := len(d.messages()); n != 0 {
		t.Errorf("%d messages after a cancelled run, want none", n)
	}
}
