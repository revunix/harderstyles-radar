// Command harderstyles-radar reads the latest releases from hardstyle.com and keeps a Spotify playlist up
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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"hardstylesync/internal/cache"
	"hardstylesync/internal/compare"
	"hardstylesync/internal/config"
	"hardstylesync/internal/corrections"
	"hardstylesync/internal/eval"
	"hardstylesync/internal/event"
	"hardstylesync/internal/hardtunes"
	"hardstylesync/internal/hdrr"
	"hardstylesync/internal/i18n"
	"hardstylesync/internal/spotify"
	"hardstylesync/internal/sync"
	"hardstylesync/internal/tui"
	"hardstylesync/internal/updater"
)

// version is set through -ldflags.
var version = "1.2.7"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "update":
			updateCmd := flag.NewFlagSet("update", flag.ExitOnError)
			checkOnly := updateCmd.Bool("check", false, "only check for updates without applying")
			force := updateCmd.Bool("force", false, "force update even if version is up to date")
			lang := updateCmd.String("lang", "", "language of the messages: de or en (default: UI_LANGUAGE, else de)")
			_ = updateCmd.Parse(os.Args[2:])

			envFile := os.Getenv("ENV_FILE")
			if envFile == "" {
				envFile = ".env"
			}
			_ = config.LoadDotEnv(envFile)
			langName := *lang
			if langName == "" {
				langName = os.Getenv("UI_LANGUAGE")
			}
			_ = i18n.SetLanguage(langName)

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			httpClient := &http.Client{Timeout: 60 * time.Second}
			runUpdate(ctx, httpClient, version, *checkOnly, *force)
			return

		case "check-update":
			checkCmd := flag.NewFlagSet("check-update", flag.ExitOnError)
			lang := checkCmd.String("lang", "", "language of the messages: de or en (default: UI_LANGUAGE, else de)")
			_ = checkCmd.Parse(os.Args[2:])

			envFile := os.Getenv("ENV_FILE")
			if envFile == "" {
				envFile = ".env"
			}
			_ = config.LoadDotEnv(envFile)
			langName := *lang
			if langName == "" {
				langName = os.Getenv("UI_LANGUAGE")
			}
			_ = i18n.SetLanguage(langName)

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			httpClient := &http.Client{Timeout: 30 * time.Second}
			runUpdate(ctx, httpClient, version, true, false)
			return
		}
	}
	auth := flag.Bool("auth", false, "log in to Spotify once and print the refresh token")
	slot := flag.Int("auth-slot", 1, "with -auth: log in for fallback app number N (SPOTIFY_CLIENT_ID_N), 1 = the main app")
	status := flag.Bool("status", false, "show the configured Spotify apps, their blocks and whether their refresh tokens still work")
	dryRun := flag.Bool("dry-run", false, "show what would change without touching the playlist")
	every := flag.Duration("every", 0, "keep running and do a full sync repeatedly, e.g. 24h (default: run once and exit)")
	everyMinutes := flag.Int("every-minutes", 0, "keep running and do a full sync every N minutes")
	at := flag.String("at", "", "run a full sync every day at HH:MM in -timezone")
	timezone := flag.String("timezone", "", "IANA time zone for -at, e.g. Europe/Berlin (required with -at)")
	watch := flag.Duration("watch", -1, "service: keep running and check hardstyle.com for new tracks this often, e.g. 15m (default: WATCH_INTERVAL)")
	lang := flag.String("lang", "", "language of the interface and the messages: de or en (default: UI_LANGUAGE, else de)")
	noTUI := flag.Bool("no-tui", false, "plain log output instead of the interface (always the case without a terminal)")
	doCompare := flag.Bool("compare", false, "compare the releases of hardstyle.com, hardtunes.com and hdrr.info with each other and exit (no Spotify needed)")
	doEvaluate := flag.Bool("evaluate", false, "measure the Spotify matching offline on what the cache learned and exit (no Spotify needed)")
	doUpdate := flag.Bool("update", false, "check for updates and update harderstyles-radar")
	doCheckUpdate := flag.Bool("check-update", false, "check if a new version is available and exit")
	forceUpdate := flag.Bool("force", false, "with -update: force update even if version is up to date")
	flag.Parse()

	if *doUpdate || *doCheckUpdate {
		envFile := os.Getenv("ENV_FILE")
		if envFile == "" {
			envFile = ".env"
		}
		_ = config.LoadDotEnv(envFile)
		name := *lang
		if name == "" {
			name = os.Getenv("UI_LANGUAGE")
		}
		_ = i18n.SetLanguage(name)

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		httpClient := &http.Client{Timeout: 60 * time.Second}
		runUpdate(ctx, httpClient, version, *doCheckUpdate, *forceUpdate)
		return
	}
	envFile := os.Getenv("ENV_FILE")
	if envFile == "" {
		envFile = ".env"
	}
	if err := config.LoadDotEnv(envFile); err != nil {
		log.Fatalf("read .env: %v", err)
	}
	flagSet := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { flagSet[f.Name] = true })
	if !flagSet["every-minutes"] && !flagSet["every"] {
		if value := os.Getenv("SYNC_EVERY_MINUTES"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 {
				log.Fatalf("invalid SYNC_EVERY_MINUTES %q: use a positive number of minutes", value)
			}
			*everyMinutes = parsed
		}
	}
	if !flagSet["at"] {
		*at = os.Getenv("SYNC_AT")
	}
	if !flagSet["timezone"] {
		*timezone = os.Getenv("SYNC_TIMEZONE")
	}
	if *everyMinutes < 0 || (*everyMinutes > 0 && *every > 0) {
		log.Fatal("use either -every or -every-minutes with a positive value")
	}
	if *everyMinutes > 0 {
		*every = time.Duration(*everyMinutes) * time.Minute
	}
	if (*at == "") != (*timezone == "") {
		log.Fatal("-at and -timezone must be provided together")
	}
	var scheduleLocation *time.Location
	if *at != "" {
		if _, err := time.Parse("15:04", *at); err != nil {
			log.Fatalf("invalid -at %q: use HH:MM", *at)
		}
		var err error
		scheduleLocation, err = time.LoadLocation(*timezone)
		if err != nil {
			log.Fatalf("invalid -timezone %q: %v", *timezone, err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	httpClient := &http.Client{Timeout: 30 * time.Second}

	// The flag wins over the environment.
	name := *lang
	if name == "" {
		name = os.Getenv("UI_LANGUAGE")
	}
	if err := i18n.SetLanguage(name); err != nil {
		log.Fatal(err)
	}
	if *doCompare {
		cfg, err := config.LoadCompare()
		if err != nil {
			log.Fatal(err)
		}
		rep, err := compare.Run(ctx, httpClient, cfg, func(format string, args ...any) { fmt.Printf(format+"\n", args...) })
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(compare.Render(rep))
		return
	}
	if *doEvaluate {
		cfg, err := config.LoadCompare()
		if err != nil {
			log.Fatal(err)
		}
		c, err := cache.Load(cfg.CacheFile)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(eval.Evaluate(c, spotify.MatchOptions{Tolerance: cfg.DateTolerance}).String())
		return
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
			Genres: uiGenres(cfg), MaxTracks: cfg.MaxTracks, DryRun: *dryRun, Every: *every, Watch: cfg.WatchInterval, At: *at, Timezone: scheduleLocation,
			MaxSearches: cfg.MaxSearches, Apps: sp.Apps, RequestDelay: cfg.RequestDelay, ContinueDelay: cfg.ContinueDelay,
			CheckUpdate: func(ctx context.Context) (*updater.CheckResult, error) {
				res, err := updater.Check(ctx, httpClient, version)
				if err != nil {
					return nil, err
				}
				if res.UpdateAvailable {
					return res, nil
				}
				return nil, nil
			},
			ApplyUpdate: func(ctx context.Context, check *updater.CheckResult, status func(string)) error {
				return updater.ApplyUpdate(ctx, httpClient, "", check, status)
			},
			Run: func(ctx context.Context, dryRun, quick bool, emit event.Emit) (sync.Result, error) {
				return sync.Run(ctx, httpClient, sp, cfg, dryRun, quick, emit)
			},
			Correct: func(id, action, uri string) error {
				return corrections.Record(cfg.CorrectionsFile, corrections.Correction{ID: id, Action: action, URI: uri})
			},
		}); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Without the interface: once and out (cron), or as a service that carries on by itself: with -every it does
	// a full sync now and then, with -watch (or WATCH_INTERVAL) it also checks for new tracks all the time.
	go func() {
		ctxCheck, cancelCheck := context.WithTimeout(ctx, 5*time.Second)
		defer cancelCheck()
		check, err := updater.Check(ctxCheck, httpClient, version)
		if err == nil && check.UpdateAvailable {
			log.Printf(i18n.T("Hinweis: Neue Version %s verfügbar (aktuell: v%s). Aktualisieren mit 'harderstyles-radar update' oder '--update'", "Notice: New version %s available (current: v%s). Update with 'harderstyles-radar update' or '--update'"), check.LatestVersion, version)
		}
	}()

	service := *every > 0 || *watch > 0 || *at != ""
	sched := sync.Schedule{Every: *every, Watch: cfg.WatchInterval, Continue: cfg.ContinueDelay, DryRun: *dryRun, At: *at, Location: scheduleLocation}
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

// uiGenres are the genres the interface can filter by: those of hardstyle.com, then those only hardtunes.com or
// hdrr.info have.
func uiGenres(cfg config.Config) []string {
	out := slices.Clone(cfg.Genres)
	var more []string
	for _, g := range cfg.HardtunesGenres {
		more = append(more, hardtunes.GenreLabel(g))
	}
	for _, p := range cfg.HdrrPlaylists {
		more = append(more, hdrr.GenreLabel(p))
	}
	for _, l := range more {
		if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, l) }) {
			out = append(out, l)
		}
	}
	return out
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

func runUpdate(ctx context.Context, hc *http.Client, currentVersion string, checkOnly, force bool) {
	fmt.Printf(i18n.T("Prüfe GitHub auf Updates (aktuell: v%s) …\n", "Checking GitHub for updates (current: v%s) …\n"), currentVersion)
	check, err := updater.Check(ctx, hc, currentVersion)
	if err != nil {
		log.Fatalf("%s: %v", i18n.T("Update-Prüfung fehlgeschlagen", "update check failed"), err)
	}

	if !check.UpdateAvailable && !force {
		fmt.Printf(i18n.T("harderstyles-radar ist bereits auf dem neuesten Stand (%s).\n", "harderstyles-radar is already up to date (%s).\n"), check.LatestVersion)
		return
	}

	if check.UpdateAvailable {
		fmt.Printf(i18n.T("Neue Version gefunden: %s (aktuell: v%s)\n", "New version found: %s (current: v%s)\n"), check.LatestVersion, currentVersion)
	} else if force {
		fmt.Printf(i18n.T("Erzwungene Aktualisierung auf %s (aktuell: v%s)\n", "Forced update to %s (current: v%s)\n"), check.LatestVersion, currentVersion)
	}

	if checkOnly {
		fmt.Println(i18n.T("Führe 'harderstyles-radar update' oder '--update' aus, um zu aktualisieren.", "Run 'harderstyles-radar update' or '--update' to install the update."))
		return
	}

	err = updater.ApplyUpdate(ctx, hc, "", check, func(status string) {
		fmt.Println(">", status)
	})
	if err != nil {
		log.Fatalf("%s: %v", i18n.T("Aktualisierung fehlgeschlagen", "update failed"), err)
	}
}
