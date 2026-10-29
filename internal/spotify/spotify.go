package spotify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hardstylesync/internal/event"
	"hardstylesync/internal/i18n"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultAPIBase  = "https://api.spotify.com/v1"
	defaultTokenURL = "https://accounts.spotify.com/api/token"
	batchSize       = 100 // max uris per add/remove request
	// maxDelay is the longest pause between two requests after Spotify asked to slow down.
	maxDelay = 10 * time.Second
	// maxRetryWait is the longest pause after a 429 the client waits out. Spotify may block an app for a
	// day; that is reported (and remembered) instead of waited for.
	maxRetryWait = 60 * time.Second
)

// RateLimitError means Spotify blocks the app for a while.
type RateLimitError struct {
	RetryAfter time.Duration
	Until      time.Time
}

func NewRateLimitError(wait time.Duration) *RateLimitError {
	return &RateLimitError{RetryAfter: wait, Until: time.Now().Add(wait)}
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf(i18n.T("Spotify begrenzt die App (Rate-Limit), erst wieder möglich in %s (ab %s)", "Spotify is limiting the app (rate limit), possible again in %s (from %s)"),
		i18n.Duration(e.RetryAfter), i18n.Stamp(e.Until))
}

type Track struct {
	URI           string
	Name          string
	Artists       []string
	ReleaseDate   string // "2026-10-29", "2026-10" or "2026", see DatePrecision
	DatePrecision string // "day", "month" or "year"
	Album         Album  // the release the track is on
}

// Album is the release a track is on: a single, an EP or an album.
type Album struct {
	ID     string
	Name   string
	Type   string // "album", "single" or "compilation"
	Tracks int    // number of tracks on it
}

// PlaylistEntry is a track in the playlist together with the time it was added.
type PlaylistEntry struct {
	URI     string
	AddedAt time.Time
}

// Credential is one Spotify app together with the refresh token of the login made through it. A refresh token
// only works with the app it was issued for, so every fallback app needs its own.
type Credential struct {
	ClientID, ClientSecret, RefreshToken string
}

type Client struct {
	http     *http.Client
	apiBase  string // overridable for tests
	tokenURL string

	emit    event.Emit                             // gets notes and the request count
	OnBlock func(clientID string, until time.Time) // called when Spotify blocks an app for long

	paceMu    sync.Mutex
	delay     time.Duration // pause between two requests
	baseWait  time.Duration
	retryWait time.Duration // first pause before a repeated request after a network error or a 5xx
	nextAt    time.Time
	requests  atomic.Int64

	mu          sync.Mutex // guards the token and the app in use
	creds       []Credential
	cur         int // index of the app in use
	blocked     map[string]time.Time
	accessToken string
	expires     time.Time
}

// Option changes the client. Tests use it to point the client at their own server.
type Option func(*Client)

// WithEndpoints sends the API and the token requests to other addresses.
func WithEndpoints(apiBase, tokenURL string) Option {
	return func(c *Client) { c.apiBase, c.tokenURL = apiBase, tokenURL }
}

// WithRetryWait sets the first pause before a request is repeated.
func WithRetryWait(d time.Duration) Option {
	return func(c *Client) { c.retryWait = d }
}

// WithEmitter sends the notes and the request count to a front end.
func WithEmitter(e event.Emit) Option {
	return func(c *Client) { c.emit = e }
}

// WithFallbacks adds apps that take over, one after the other, when the app in use is blocked.
func WithFallbacks(creds ...Credential) Option {
	return func(c *Client) { c.creds = append(c.creds, creds...) }
}

func New(c *http.Client, id, secret, refresh string, opts ...Option) *Client {
	s := &Client{http: c, creds: []Credential{{id, secret, refresh}}, blocked: map[string]time.Time{}, apiBase: defaultAPIBase, tokenURL: defaultTokenURL, retryWait: time.Second}
	for _, o := range opts {
		o(s)
	}
	return s
}

// SetBlocked tells the client that Spotify blocks an app until a time, and moves to the first app that is not
// blocked. A zero time clears the block.
func (s *Client) SetBlocked(clientID string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if until.IsZero() {
		delete(s.blocked, clientID)
	} else {
		s.blocked[clientID] = until
	}
	if i := s.nextFree(-1); i >= 0 && i != s.cur {
		s.cur, s.accessToken = i, ""
	}
}

// nextFree returns the index of the first app after index from that is not blocked, or -1. The caller holds mu.
func (s *Client) nextFree(from int) int {
	for i := from + 1; i < len(s.creds); i++ {
		if time.Now().After(s.blocked[s.creds[i].ClientID]) {
			return i
		}
	}
	return -1
}

// switchApp marks the app in use as blocked until a time and moves to the next free one. It returns the id of
// the app now in use, or false when every app is blocked, in which case the earliest end of a block is returned.
func (s *Client) switchApp(until time.Time) (string, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocked[s.creds[s.cur].ClientID] = until
	if i := s.nextFree(-1); i >= 0 {
		s.cur, s.accessToken = i, ""
		return s.creds[i].ClientID, time.Time{}, true
	}
	earliest := until
	for _, c := range s.creds {
		if t := s.blocked[c.ClientID]; t.Before(earliest) {
			earliest = t
		}
	}
	return "", earliest, false
}

// AppStatus is what the client knows about one of its apps.
type AppStatus struct {
	ClientID     string
	InUse        bool      // the app the next request goes out with
	BlockedUntil time.Time // zero when Spotify does not block it
}

// Apps returns the apps in the order they are used.
func (s *Client) Apps() []AppStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AppStatus, len(s.creds))
	for i, c := range s.creds {
		out[i] = AppStatus{ClientID: c.ClientID, InUse: i == s.cur}
		if until := s.blocked[c.ClientID]; time.Now().Before(until) {
			out[i].BlockedUntil = until
		}
	}
	return out
}

// TokenCheck is the answer of Spotify to a refresh with one app.
type TokenCheck struct {
	Scope     string        // the permissions granted with the login
	ExpiresIn time.Duration // lifetime of the access token; Spotify tells nothing about the refresh token
	Rotated   bool          // Spotify sent a new refresh token along
}

// CheckToken asks Spotify for an access token with one app, to see whether its refresh token still works. It
// goes to the token endpoint only, not to the Web API whose rate limit blocks apps.
func CheckToken(ctx context.Context, hc *http.Client, cr Credential, opts ...Option) (TokenCheck, error) {
	s := New(hc, cr.ClientID, cr.ClientSecret, cr.RefreshToken, opts...)
	var out struct {
		Scope        string `json:"scope"`
		ExpiresIn    int    `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {cr.RefreshToken}}
	if err := s.tokenRequest(ctx, form, &out); err != nil {
		return TokenCheck{}, err
	}
	return TokenCheck{Scope: out.Scope, ExpiresIn: time.Duration(out.ExpiresIn) * time.Second, Rotated: out.RefreshToken != "" && out.RefreshToken != cr.RefreshToken}, nil
}

func (s *Client) currentID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creds[s.cur].ClientID
}

// SetEmitter sets where the notes and the request count of the next runs go.
func (s *Client) SetEmitter(e event.Emit) { s.emit = e }

// SetDelay sets the pause between two requests. Spotify limits the number of requests in a time window, so
// the tool never fires them in a burst.
func (s *Client) SetDelay(d time.Duration) {
	s.paceMu.Lock()
	defer s.paceMu.Unlock()
	s.delay, s.baseWait = d, d
}

// Delay returns the pause between two requests.
func (s *Client) Delay() time.Duration {
	s.paceMu.Lock()
	defer s.paceMu.Unlock()
	return s.delay
}

// ResetRequests starts the count of requests of a run.
func (s *Client) ResetRequests() { s.requests.Store(0) }

// Requests returns the number of requests since ResetRequests.
func (s *Client) Requests() int { return int(s.requests.Load()) }

// pace waits until the next request may go out.
func (s *Client) pace(ctx context.Context) error {
	s.paceMu.Lock()
	wait := max(0, time.Until(s.nextAt))
	s.nextAt = time.Now().Add(wait + s.delay)
	s.paceMu.Unlock()
	if wait == 0 {
		return nil
	}
	select {
	case <-time.After(wait):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// slowDown doubles the pause between requests for the rest of the run.
func (s *Client) slowDown() {
	s.paceMu.Lock()
	defer s.paceMu.Unlock()
	s.delay = min(max(s.delay*2, time.Second), maxDelay)
}

// basicAuth is called with mu held (or before the client is shared).
func (s *Client) basicAuth() string {
	c := s.creds[s.cur]
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.ClientID+":"+c.ClientSecret))
}

// token returns a valid access token, refreshing it when needed.
func (s *Client) token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accessToken != "" && time.Now().Before(s.expires.Add(-30*time.Second)) {
		return s.accessToken, nil
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.creds[s.cur].RefreshToken}}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := s.tokenRequest(ctx, form, &out); err != nil {
		return "", err
	}
	s.accessToken = out.AccessToken
	s.expires = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return s.accessToken, nil
}

func (s *Client) tokenRequest(ctx context.Context, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", s.basicAuth())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("spotify token: HTTP %d: %s", resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}

// do sends an authorized API request, paced, and retries when rate limited.
func (s *Client) do(ctx context.Context, method, path string, query url.Values, payload, out any) error {
	var body []byte
	if payload != nil {
		var err error
		if body, err = json.Marshal(payload); err != nil {
			return err
		}
	}
	u := s.apiBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	transient := 0
	for attempt := 0; ; attempt++ {
		tok, err := s.token(ctx)
		if err != nil {
			return err
		}
		if err := s.pace(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := s.http.Do(req)
		if err != nil {
			if s.retry(ctx, method, &transient, err.Error()) {
				continue
			}
			return err
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		s.emit.Send(event.Event{Kind: event.EvRequests, Done: int(s.requests.Add(1))})

		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			seconds, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			wait := time.Duration(seconds+1) * time.Second
			if wait > maxRetryWait || attempt >= 3 {
				limit := NewRateLimitError(wait)
				id := s.currentID()
				if s.OnBlock != nil {
					s.OnBlock(id, limit.Until)
				}
				next, earliest, ok := s.switchApp(limit.Until)
				if !ok {
					return &RateLimitError{RetryAfter: time.Until(earliest), Until: earliest}
				}
				s.paceMu.Lock()
				s.delay = s.baseWait // the next app is not throttled
				s.paceMu.Unlock()
				s.emit.Logf(i18n.T("App %s ist gesperrt, weiter mit einer anderen App (%s)", "App %s is blocked, switching to another app (%s)"), id, next)
				attempt = -1 // the next app gets its own tries
				continue
			}
			s.slowDown()
			s.emit.Logf(i18n.T("Spotify bremst, warte %d s und gehe langsamer vor", "Spotify is throttling, waiting %d s and slowing down"), seconds+1)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		case resp.StatusCode >= 500 && s.retry(ctx, method, &transient, fmt.Sprintf("HTTP %d", resp.StatusCode)):
			// waited, try again
		case resp.StatusCode == http.StatusUnauthorized && attempt < 1:
			s.mu.Lock()
			s.accessToken = "" // expired early, refresh and retry once
			s.mu.Unlock()
		case resp.StatusCode >= 300:
			return fmt.Errorf("spotify %s %s: HTTP %d: %s", method, path, resp.StatusCode, data)
		default:
			if out != nil && len(data) > 0 {
				return json.Unmarshal(data, out)
			}
			return nil
		}
	}
}

// retry decides whether a request that failed for a passing reason (network error, 5xx) is repeated, and waits
// before it. Only requests that are safe to repeat are: a repeated POST could add the tracks twice.
func (s *Client) retry(ctx context.Context, method string, tries *int, why string) bool {
	if method == http.MethodPost || *tries >= 2 || ctx.Err() != nil {
		return false
	}
	wait := s.retryWait << *tries
	*tries++
	s.emit.Logf(i18n.T("Spotify antwortet nicht richtig (%s), neuer Versuch in %s", "Spotify is not answering properly (%s), trying again in %s"), why, wait.Round(time.Millisecond))
	select {
	case <-time.After(wait):
		return true
	case <-ctx.Done():
		return false
	}
}

// Search looks up tracks. Since Feb 2026 the maximum limit is 10.
func (s *Client) Search(ctx context.Context, q string) ([]Track, error) {
	var res struct {
		Tracks struct {
			Items []struct {
				URI     string `json:"uri"`
				Name    string `json:"name"`
				Artists []struct {
					Name string `json:"name"`
				} `json:"artists"`
				Album struct {
					ID          string `json:"id"`
					Name        string `json:"name"`
					Type        string `json:"album_type"`
					TotalTracks int    `json:"total_tracks"`
					ReleaseDate string `json:"release_date"`
					Precision   string `json:"release_date_precision"`
				} `json:"album"`
			} `json:"items"`
		} `json:"tracks"`
	}
	err := s.do(ctx, http.MethodGet, "/search", url.Values{"q": {q}, "type": {"track"}, "limit": {"10"}}, nil, &res)
	if err != nil {
		return nil, err
	}
	var out []Track
	for _, it := range res.Tracks.Items {
		t := Track{URI: it.URI, Name: it.Name, ReleaseDate: it.Album.ReleaseDate, DatePrecision: it.Album.Precision,
			Album: Album{ID: it.Album.ID, Name: it.Album.Name, Type: it.Album.Type, Tracks: it.Album.TotalTracks}}
		for _, a := range it.Artists {
			t.Artists = append(t.Artists, a.Name)
		}
		out = append(out, t)
	}
	return out, nil
}

// AlbumTracks returns the tracks of an album, in order. Only the URI, the name and the artists are filled in;
// the album and its date are the caller's.
func (s *Client) AlbumTracks(ctx context.Context, albumID string) ([]Track, error) {
	var out []Track
	for offset := 0; ; offset += 50 {
		var page struct {
			Items []struct {
				URI     string `json:"uri"`
				Name    string `json:"name"`
				Artists []struct {
					Name string `json:"name"`
				} `json:"artists"`
			} `json:"items"`
			Next string `json:"next"`
		}
		q := url.Values{"limit": {"50"}, "offset": {strconv.Itoa(offset)}}
		if err := s.do(ctx, http.MethodGet, "/albums/"+url.PathEscape(albumID)+"/tracks", q, nil, &page); err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			t := Track{URI: it.URI, Name: it.Name}
			for _, a := range it.Artists {
				t.Artists = append(t.Artists, a.Name)
			}
			out = append(out, t)
		}
		if page.Next == "" || len(page.Items) == 0 {
			return out, nil
		}
	}
}
