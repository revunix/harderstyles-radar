package spotify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"hardstylesync/internal/i18n"
	"net"
	"net/http"
	"net/url"
	"time"
)

const (
	redirectURI = "http://127.0.0.1:8888/callback"
	scopes      = "playlist-modify-public playlist-modify-private playlist-read-private"
)

// Auth performs the one-time Authorization Code login and prints the refresh token.
// The redirect URI above has to be registered in the Spotify developer dashboard.
func Auth(ctx context.Context, hc *http.Client, clientID, clientSecret string) error {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	state := hex.EncodeToString(b)

	ln, err := net.Listen("tcp", "127.0.0.1:8888")
	if err != nil {
		return err
	}
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		switch {
		case q.Get("state") != state:
			errCh <- fmt.Errorf("state mismatch")
		case q.Get("error") != "":
			errCh <- fmt.Errorf("spotify: %s", q.Get("error"))
		default:
			codeCh <- q.Get("code")
		}
		fmt.Fprintln(w, i18n.T("Fertig, du kannst diesen Tab schließen.", "Done, you can close this tab."))
	})}
	go srv.Serve(ln)
	defer srv.Close()

	authURL := "https://accounts.spotify.com/authorize?" + url.Values{
		"client_id":     {clientID},
		"response_type": {"code"},
		"redirect_uri":  {redirectURI},
		"scope":         {scopes},
		"state":         {state},
	}.Encode()
	fmt.Println(i18n.T("Öffne diese Adresse im Browser und erlaube den Zugriff:\n\n", "Open this URL in your browser and allow access:\n\n") + authURL + "\n")

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return err
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("timed out waiting for login")
	case <-ctx.Done():
		return ctx.Err()
	}

	sp := New(hc, clientID, clientSecret, "")
	var out struct {
		RefreshToken string `json:"refresh_token"`
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}}
	if err := sp.tokenRequest(ctx, form, &out); err != nil {
		return err
	}
	fmt.Println(i18n.T("Refresh-Token (als SPOTIFY_REFRESH_TOKEN eintragen):\n\n", "Refresh token (set as SPOTIFY_REFRESH_TOKEN):\n\n") + out.RefreshToken)
	return nil
}
