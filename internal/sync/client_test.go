package sync

import (
	"context"
	"errors"
	"fmt"
	"hardstylesync/internal/event"
	"hardstylesync/internal/spotify"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestShortRateLimitIsWaitedOutAndSlowsDown(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
			return
		}
		if calls++; calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"tracks":{"items":[]}}`)
	}))
	defer srv.Close()
	sp := spotify.New(srv.Client(), "id", "secret", "refresh", spotify.WithEndpoints(srv.URL, srv.URL+"/token"))
	var logged []string
	sp.SetEmitter(func(ev event.Event) {
		if ev.Kind == event.EvLog {
			logged = append(logged, ev.Msg)
		}
	})
	if _, err := sp.Search(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "bremst") {
		t.Errorf("log = %v, want a note about the wait", logged)
	}
	if sp.Delay() < time.Second {
		t.Errorf("delay = %s, after a 429 the tool must go slower", sp.Delay())
	}
}

func TestRequestsAreSpacedOut(t *testing.T) {
	f := &fakeSpotify{}
	sp := newFakeClient(t, f)
	sp.SetDelay(60 * time.Millisecond)
	start := time.Now()
	for i := 0; i < 4; i++ {
		if _, err := sp.Search(context.Background(), "T1 A"); err != nil {
			t.Fatal(err)
		}
	}
	if took := time.Since(start); took < 170*time.Millisecond {
		t.Errorf("4 requests took %s, with a delay of 60 ms they need at least 180 ms", took)
	}
	if sp.Requests() != 4 {
		t.Errorf("requests = %d", sp.Requests())
	}
}

func TestPassingServerErrorsAreRetried(t *testing.T) {
	f := &fakeSpotify{failNext: 2}
	sp := newFakeClient(t, f)
	var logged []string
	sp.SetEmitter(func(ev event.Event) {
		if ev.Kind == event.EvLog {
			logged = append(logged, ev.Msg)
		}
	})
	found, err := sp.Search(context.Background(), "T1 A")
	if err != nil || len(found) != 1 {
		t.Fatalf("found=%v err=%v, two 500s in a row must be survived", found, err)
	}
	if len(logged) != 2 || !strings.Contains(logged[0], "neuer Versuch") {
		t.Errorf("log = %v", logged)
	}
	f.mu.Lock()
	f.failNext = 3
	f.mu.Unlock()
	if _, err := sp.Search(context.Background(), "T1 A"); err == nil {
		t.Error("after two retries the error must be reported")
	}
}

func TestInsertIsNeverRepeated(t *testing.T) {
	f := &fakeSpotify{failPost: true}
	sp := newFakeClient(t, f)
	if _, err := sp.InsertAtTop(context.Background(), "p", []string{uri("T1")}); err == nil {
		t.Fatal("want the error")
	}
	f.mu.Lock()
	posts := f.posts
	f.mu.Unlock()
	if posts != 1 {
		t.Errorf("%d inserts, a repeated POST could add the tracks twice", posts)
	}
}

func TestBlockedAppIsReplacedByTheFallback(t *testing.T) {
	var searches []string // the app of each search request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _, _ := r.BasicAuth()
		if r.URL.Path == "/token" {
			fmt.Fprintf(w, `{"access_token":"tok-%s","expires_in":3600}`, user)
			return
		}
		searches = append(searches, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") == "Bearer tok-a" || r.Header.Get("Authorization") == "Bearer tok-b" {
			w.Header().Set("Retry-After", "86400")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"tracks":{"items":[]}}`)
	}))
	defer srv.Close()
	sp := spotify.New(srv.Client(), "a", "s", "r", spotify.WithEndpoints(srv.URL, srv.URL+"/token"),
		spotify.WithFallbacks(spotify.Credential{ClientID: "b", ClientSecret: "s", RefreshToken: "r"}, spotify.Credential{ClientID: "c", ClientSecret: "s", RefreshToken: "r"}))
	blocked := map[string]bool{}
	sp.OnBlock = func(id string, until time.Time) { blocked[id] = true }
	if _, err := sp.Search(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if !blocked["a"] || !blocked["b"] || blocked["c"] || len(searches) != 3 {
		t.Errorf("blocked=%v searches=%v, want a and b blocked and c answering", blocked, searches)
	}
	// The next request goes straight to the app that works.
	if _, err := sp.Search(context.Background(), "y"); err != nil || len(searches) != 4 || searches[3] != "Bearer tok-c" {
		t.Errorf("err=%v searches=%v", err, searches)
	}
}

func TestAllAppsBlockedReportsTheEarliestEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
			return
		}
		w.Header().Set("Retry-After", "86400")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	sp := spotify.New(srv.Client(), "a", "s", "r", spotify.WithEndpoints(srv.URL, srv.URL+"/token"),
		spotify.WithFallbacks(spotify.Credential{ClientID: "b", ClientSecret: "s", RefreshToken: "r"}))
	_, err := sp.Search(context.Background(), "x")
	var limit *spotify.RateLimitError
	if !errors.As(err, &limit) || limit.RetryAfter < 23*time.Hour {
		t.Fatalf("err = %v, want the rate limit once every app is blocked", err)
	}
}

func TestCheckToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("refresh_token") != "good" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Refresh token revoked"}`)
			return
		}
		fmt.Fprint(w, `{"access_token":"t","expires_in":3600,"scope":"playlist-modify-public","refresh_token":"newer"}`)
	}))
	defer srv.Close()
	ep := spotify.WithEndpoints(srv.URL, srv.URL)
	check, err := spotify.CheckToken(context.Background(), srv.Client(), spotify.Credential{ClientID: "a", ClientSecret: "s", RefreshToken: "good"}, ep)
	if err != nil || check.Scope != "playlist-modify-public" || check.ExpiresIn != time.Hour || !check.Rotated {
		t.Errorf("check = %+v, err = %v", check, err)
	}
	if _, err := spotify.CheckToken(context.Background(), srv.Client(), spotify.Credential{ClientID: "a", ClientSecret: "s", RefreshToken: "old"}, ep); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Errorf("a revoked token must be reported, err = %v", err)
	}
}

func TestAppsShowsBlocksAndTheAppInUse(t *testing.T) {
	sp := spotify.New(http.DefaultClient, "a", "s", "r", spotify.WithFallbacks(spotify.Credential{ClientID: "b"}))
	until := time.Now().Add(time.Hour)
	sp.SetBlocked("a", until)
	apps := sp.Apps()
	if len(apps) != 2 || apps[0].InUse || !apps[0].BlockedUntil.Equal(until) || !apps[1].InUse || !apps[1].BlockedUntil.IsZero() {
		t.Errorf("apps = %+v", apps)
	}
}
