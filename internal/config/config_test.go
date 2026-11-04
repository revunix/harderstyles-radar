package config

import (
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/hardtunes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGeneralListSetting(t *testing.T) {
	for _, c := range []struct {
		genres string
		want   []string
		pages  int
	}{
		{"any", []string{hardstyle.AnyGenre}, 25}, // 500 tracks at 20 per page: the most one list may need
		{"ANY", []string{hardstyle.AnyGenre}, 25},
		{"*", []string{hardstyle.AnyGenre}, 25},
		{"Hardstyle", []string{"Hardstyle"}, 25},
		{"Hardstyle, Uptempo", []string{"Hardstyle", "Uptempo"}, 25},
		{"any, Hardstyle", []string{hardstyle.AnyGenre}, 25}, // the general list already has them all
	} {
		t.Setenv("GENRES", c.genres)
		t.Setenv("PAGES", "")
		t.Setenv("SPOTIFY_CLIENT_ID", "x")
		t.Setenv("SPOTIFY_CLIENT_SECRET", "x")
		cfg, err := Load(false)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cfg.Genres, c.want) || cfg.Pages != c.pages {
			t.Errorf("GENRES=%q: genres=%q pages=%d, want %q / %d", c.genres, cfg.Genres, cfg.Pages, c.want, c.pages)
		}
	}
	t.Setenv("GENRES", "any")
	t.Setenv("PAGES", "5")
	if cfg, _ := Load(false); cfg.Pages != 5 {
		t.Errorf("PAGES must win, got %d", cfg.Pages)
	}
}

func TestPagesFollowMaxTracksAndCompareLooksShort(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_ID", "x")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "x")
	t.Setenv("PAGES", "")
	t.Setenv("MAX_TRACKS", "300")
	if cfg, err := Load(false); err != nil || cfg.Pages != 15 {
		t.Errorf("MAX_TRACKS=300: pages=%d err=%v, want 15", cfg.Pages, err)
	}
	if cfg, err := LoadCompare(); err != nil || cfg.Pages != 2 {
		t.Errorf("-compare: pages=%d err=%v, want 2", cfg.Pages, err)
	}
}

func TestAllGenresAreTheGenresOnly(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_ID", "x")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "x")
	t.Setenv("GENRES", "all")
	if cfg, _ := Load(false); !slices.Equal(cfg.Genres, allGenres) {
		t.Errorf("GENRES=all: %q, want the genres without a genre named all", cfg.Genres)
	}
}

func TestGenreMinAndEvalSamples(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_ID", "x")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "x")
	if cfg, err := Load(false); err != nil || cfg.GenreMin != 0 || cfg.EvalSamples != 5 {
		t.Errorf("defaults: GenreMin=%d EvalSamples=%d err=%v", cfg.GenreMin, cfg.EvalSamples, err)
	}
	t.Setenv("GENRE_MIN", "-1")
	if _, err := Load(false); err == nil {
		t.Error("a negative GENRE_MIN must be reported")
	}
}

func TestPlaylistOrder(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_ID", "x")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "x")
	if cfg, err := Load(false); err != nil || cfg.Order != OrderNewest {
		t.Errorf("default: %q, %v", cfg.Order, err)
	}
	t.Setenv("PLAYLIST_ORDER", " Oldest ")
	if cfg, err := Load(false); err != nil || cfg.Order != OrderOldest {
		t.Errorf("Oldest: %q, %v", cfg.Order, err)
	}
	t.Setenv("PLAYLIST_ORDER", "random")
	if _, err := Load(false); err == nil || !strings.Contains(err.Error(), "PLAYLIST_ORDER") {
		t.Errorf("an unknown order must be reported, err = %v", err)
	}
}

func TestDiscordWebhook(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_ID", "x")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "x")
	t.Setenv("DISCORD_WEBHOOK_URL", " https://discord.com/api/webhooks/1/token ")
	cfg, err := Load(false)
	if err != nil || cfg.DiscordWebhook != "https://discord.com/api/webhooks/1/token" {
		t.Fatalf("webhook = %q, err = %v", cfg.DiscordWebhook, err)
	}

	t.Setenv("DISCORD_WEBHOOK_URL", "discord.com/api/webhooks/1/token")
	if _, err := Load(false); err == nil || !strings.Contains(err.Error(), "DISCORD_WEBHOOK_URL") {
		t.Errorf("a URL without a scheme must be reported, err = %v", err)
	}
	// The error must not repeat the address, it contains the token.
	if _, err := Load(false); err != nil && strings.Contains(err.Error(), "token") {
		t.Errorf("the error repeats the webhook address: %v", err)
	}

	t.Setenv("DISCORD_WEBHOOK_URL", "")
	if cfg, err := Load(false); err != nil || cfg.DiscordWebhook != "" {
		t.Errorf("without a webhook: %q, %v", cfg.DiscordWebhook, err)
	}
}

func TestLoadDotEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\nDOT_A=one\nexport DOT_B=\"two words\"\nDOT_C='x' \nDOT_D=val # note\nDOT_KEEP=fromfile\nDOT_E=   # only a comment\n\nbroken line\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOT_KEEP", "fromenv")
	for _, k := range []string{"DOT_A", "DOT_B", "DOT_C", "DOT_D", "DOT_E"} {
		t.Setenv(k, "") // registers cleanup
		os.Unsetenv(k)
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"DOT_A": "one", "DOT_B": "two words", "DOT_C": "x", "DOT_D": "val", "DOT_E": "", "DOT_KEEP": "fromenv"}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Errorf("missing file should be fine, got %v", err)
	}
}

func TestFallbackApps(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_ID", "a")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "as")
	t.Setenv("SPOTIFY_REFRESH_TOKEN", "ar")
	t.Setenv("SPOTIFY_PLAYLIST_ID", "p")
	cfg, err := Load(true)
	if err != nil || len(cfg.Credentials) != 1 {
		t.Fatalf("no fallbacks: err=%v, %d apps", err, len(cfg.Credentials))
	}

	t.Setenv("SPOTIFY_CLIENT_ID_2", "b")
	t.Setenv("SPOTIFY_CLIENT_SECRET_2", "bs")
	t.Setenv("SPOTIFY_REFRESH_TOKEN_2", "br")
	t.Setenv("SPOTIFY_CLIENT_ID_3", "c")
	t.Setenv("SPOTIFY_CLIENT_SECRET_3", "cs")
	t.Setenv("SPOTIFY_REFRESH_TOKEN_3", "cr")
	cfg, err = Load(true)
	if err != nil || len(cfg.Credentials) != 3 || cfg.Credentials[2].ClientID != "c" || cfg.Credentials[1].RefreshToken != "br" {
		t.Fatalf("err=%v, apps=%+v", err, cfg.Credentials)
	}

	t.Setenv("SPOTIFY_REFRESH_TOKEN_3", "")
	if _, err = Load(true); err == nil || !strings.Contains(err.Error(), "SPOTIFY_REFRESH_TOKEN_3") {
		t.Errorf("an incomplete fallback must be reported, err = %v", err)
	}
	if _, err = Load(false); err != nil {
		t.Errorf("-auth does not need the refresh token yet: %v", err)
	}
}

func TestHardtunesGenres(t *testing.T) {
	t.Setenv("SPOTIFY_CLIENT_ID", "x")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "x")
	cfg, err := Load(false)
	if err != nil || !slices.Equal(cfg.HardtunesGenres, hardtunes.Genres) {
		t.Errorf("default = all six hardtunes genres, got %v, err = %v", cfg.HardtunesGenres, err)
	}
	t.Setenv("HARDTUNES_GENRES", "hardstyle, terror")
	if cfg, err = Load(false); err != nil || !slices.Equal(cfg.HardtunesGenres, []string{"hardstyle", "terror"}) {
		t.Errorf("HARDTUNES_GENRES must win, got %v, err = %v", cfg.HardtunesGenres, err)
	}
	t.Setenv("HARDTUNES_GENRES", "none")
	if cfg, err = Load(false); err != nil || len(cfg.HardtunesGenres) != 0 {
		t.Errorf("none switches hardtunes.com off, got %v, err = %v", cfg.HardtunesGenres, err)
	}
}

func TestLoadCompareNeedsNoSpotify(t *testing.T) {
	cfg, err := LoadCompare()
	if err != nil {
		t.Fatalf("compare needs no credentials: %v", err)
	}
	if len(cfg.HardtunesGenres) != 6 || len(cfg.Genres) != 1 || cfg.Pages < 1 {
		t.Errorf("sources and pages expected: %+v", cfg)
	}
}
