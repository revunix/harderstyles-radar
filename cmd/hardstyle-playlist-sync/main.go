// Command hardstyle-playlist-sync reads the latest releases from hardstyle.com and keeps a Spotify playlist up
// to date (max N tracks, oldest are removed first).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hardstylesync/internal/cache"
	"hardstylesync/internal/config"
	"hardstylesync/internal/event"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/spotify"
	"hardstylesync/internal/sync"
	"hardstylesync/internal/tui"
)

// version is set through -ldflags.
var version = "0.1.0"

func main() {
	auth := flag.Bool("auth", false, "log in to Spotify once and print the refresh token")
	slot := flag.Int("auth-slot", 1, "with -auth: log in for fallback app number N (SPOTIFY_CLIENT_ID_N), 1 = the main app")
	status := flag.Bool("status", false, "show the configured Spotify apps, their blocks and whether their refresh tokens still work")
	dryRun := flag.Bool("dry-run", false, "show what would change without touching the playlist")
	every := flag.Duration("every", 0, "keep running and do a full sync repeatedly, e.g. 24h (default: run once and exit)")
	watch := flag.Duration("watch", -1, "service: keep running and check hardstyle.com for new tracks this often, e.g. 15m (default: WATCH_INTERVAL)")
	lang := flag.String("lang", "", "language of the interface and the messages: de or en (default: UI_LANGUAGE, else de)")
	noTUI := flag.Bool("no-tui", false, "plain log output instead of the interface (always the case without a terminal)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	httpClient := &http.Client{Timeout: 30 * time.Second}

	envFile := os.Getenv("ENV_FILE")
	if envFile == "" {
		envFile = ".env"
	}
	if err := config.LoadDotEnv(envFile); err != nil {
		log.Fatalf("read .env: %v", err)
	}
	// The flag wins over the environment.
	name := *lang
	if name == "" {
		name = os.Getenv("UI_LANGUAGE")
	}
	if err := i18n.SetLanguage(name); err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load(!*auth && !*status)
	if err != nil {
		log.Fatal(err)
	}
	if *auth {
		if *slot < 1 || *slot > len(cfg.Credentials) {
			log.Fatalf("-auth-slot %d: no such app, %d configured", *slot, len(cfg.Credentials))
		}
		cr := cfg.Credentials[*slot-1]
		if err := spotify.Auth(ctx, httpClient, cr.ClientID, cr.ClientSecret); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *status {
		if !printStatus(ctx, httpClient, cfg) {
			os.Exit(1)
		}
		return
	}

	if *watch >= 0 {
		cfg.WatchInterval = *watch
	}
	sp := spotify.New(httpClient, cfg.ClientID, cfg.ClientSecret, cfg.RefreshToken, spotify.WithFallbacks(cfg.Credentials[1:]...))
	if !*noTUI && isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		tui.Version = version
		if err := tui.Run(ctx, tui.Options{
			Genres: cfg.Genres, Pages: cfg.Pages, MaxTracks: cfg.MaxTracks, DryRun: *dryRun, Every: *every, Watch: cfg.WatchInterval,
			MaxSearches: cfg.MaxSearches, Apps: sp.Apps, RequestDelay: cfg.RequestDelay, ContinueDelay: cfg.ContinueDelay,
			Run: func(ctx context.Context, dryRun, quick bool, emit event.Emit) (sync.Result, error) {
				return sync.Run(ctx, httpClient, sp, cfg, dryRun, quick, emit)
			},
		}); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Without the interface: once and out (cron), or as a service that carries on by itself: with -every it does
	// a full sync now and then, with -watch (or WATCH_INTERVAL) it also checks for new tracks all the time.
	service := *every > 0 || *watch > 0
	sched := sync.Schedule{Every: *every, Watch: cfg.WatchInterval, Continue: cfg.ContinueDelay, DryRun: *dryRun}
	if !service {
		sched.Watch = 0
	}
	failures := 0
	quick := false
	lastFull := time.Now()
	for {
		res, err := sync.Run(ctx, httpClient, sp, cfg, *dryRun, quick, logEmit)
		if err != nil {
			log.Printf(i18n.T("Lauf fehlgeschlagen: %v", "sync failed: %v"), err)
			if !errors.Is(err, context.Canceled) {
				failures++
			}
		} else {
			failures = 0
			if !quick {
				lastFull = time.Now()
			}
		}
		if !service {
			if res.Deferred > 0 && err == nil {
				log.Printf(i18n.T("%d Releases warten auf den nächsten Lauf (Suchlimit): starte das Tool erneut oder nutze -every oder -watch", "%d releases are waiting for the next run (search limit): start the tool again or use -every or -watch"), res.Deferred)
			}
			if err != nil {
				os.Exit(1)
			}
			return
		}
		wait, why, nextQuick := sched.Next(res, err, failures, time.Since(lastFull))
		if wait <= 0 {
			return
		}
		quick = nextQuick
		log.Printf(i18n.T("nächster Lauf in %s (%s)", "next run in %s (%s)"), i18n.Duration(wait), why)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

// logEmit prints the log events, for runs without the interface.
func logEmit(ev event.Event) {
	if ev.Kind == event.EvLog {
		log.Print(ev.Msg)
	}
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// printStatus lists the Spotify apps: the block Spotify set for each (from the cache) and whether its refresh
// token still gets an access token. It reports false when an app does not work.
func printStatus(ctx context.Context, hc *http.Client, cfg config.Config) bool {
	c, err := cache.Load(cfg.CacheFile)
	if err != nil {
		log.Printf(i18n.T("Zwischenspeicher nicht lesbar: %v", "Cache not readable: %v"), err)
	}
	if len(cfg.Credentials) > 0 {
		c.AdoptLegacyBlock(cfg.Credentials[0].ClientID)
	}
	fmt.Printf(i18n.T("%d Spotify-App(s) hinterlegt\n\n", "%d Spotify app(s) configured\n\n"), len(cfg.Credentials))
	ok := true
	for i, cr := range cfg.Credentials {
		id := cr.ClientID
		if len(id) > 8 {
			id = id[:8] + "…"
		}
		name := i18n.T("Haupt-App", "main app")
		if i > 0 {
			name = fmt.Sprintf("Fallback _%d", i+1)
		}
		fmt.Printf("#%d  %s  (%s)\n", i+1, id, name)

		if until := c.BlockedUntil(cr.ClientID); until.IsZero() {
			fmt.Println(i18n.T("    Sperre:        keine", "    Block:         none"))
		} else {
			fmt.Printf(i18n.T("    Sperre:        bis %s (noch %s)\n", "    Block:         until %s (%s left)\n"), i18n.Stamp(until), i18n.Duration(time.Until(until).Round(time.Minute)))
		}

		if cr.RefreshToken == "" {
			ok = false
			fmt.Printf(i18n.T("    Refresh-Token: fehlt, holen mit -auth -auth-slot %d\n", "    Refresh token: missing, get one with -auth -auth-slot %d\n"), i+1)
			continue
		}
		check, err := spotify.CheckToken(ctx, hc, cr)
		if err != nil {
			ok = false
			fmt.Printf(i18n.T("    Refresh-Token: geht nicht (%v)\n", "    Refresh token: does not work (%v)\n"), err)
			fmt.Printf(i18n.T("                   neu holen mit -auth -auth-slot %d\n", "                   get a new one with -auth -auth-slot %d\n"), i+1)
			continue
		}
		fmt.Println(i18n.T("    Refresh-Token: gültig", "    Refresh token: valid"))
		if check.Rotated {
			fmt.Printf("%s %d\n", i18n.T("                   Spotify hat einen neuen mitgeschickt; falls der alte später nicht mehr geht: -auth -auth-slot", "                   Spotify sent a new one along; should the old one stop working: -auth -auth-slot"), i+1)
		}
		fmt.Printf(i18n.T("    Rechte:        %s\n", "    Scopes:        %s\n"), check.Scope)
	}
	fmt.Println(i18n.T("\nEine Restlaufzeit des Refresh-Tokens nennt Spotify nicht, die Prüfung oben zeigt nur, ob er jetzt geht.\nUngültig wird er, wenn der Zugriff entzogen wird (spotify.com/account/apps) oder die App im Dashboard\ndas Konto aus der User-Liste verliert.",
		"\nSpotify does not give a remaining lifetime for refresh tokens, the check above only shows whether it works now.\nIt stops working when access is revoked (spotify.com/account/apps) or the app loses the account\nfrom its user list in the dashboard."))
	return ok
}
