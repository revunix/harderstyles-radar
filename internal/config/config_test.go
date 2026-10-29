package config

import (
	"hardstylesync/internal/hardstyle"
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
		{"any", []string{hardstyle.AnyGenre}, 17}, // 500 tracks at 30 per page
		{"ANY", []string{hardstyle.AnyGenre}, 17},
		{"*", []string{hardstyle.AnyGenre}, 17},
		{"Hardstyle", []string{"Hardstyle"}, 2},
		{"Hardstyle, Uptempo", []string{"Hardstyle", "Uptempo"}, 2},
		{"any, Hardstyle", []string{hardstyle.AnyGenre}, 17}, // the general list already has them all
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
