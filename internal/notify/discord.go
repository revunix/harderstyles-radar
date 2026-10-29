// Package notify sends the summary of a run to a Discord webhook.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/i18n"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxListed is how many of the new tracks the message names; the rest is counted.
const maxListed = 10

// The colours of the embed: green when the playlist changed, grey when nothing happened, red for a failure.
const (
	colorChanged = 0x1db954
	colorQuiet   = 0x5865f2
	colorError   = 0xed4245
)

// RunSummary is what a run did, as the message shows it.
type RunSummary struct {
	PlaylistID string
	At         time.Time
	After      int // tracks in the playlist afterwards
	Added      int // tracks that came into the playlist
	Removed    int // tracks that dropped out at the end
	Matched    int // releases found on Spotify
	Missing    int // releases not found on Spotify
	Deferred   int // releases waiting for the next run
	Requests   int // Spotify requests of the run
	// NewTracks are the releases that came into the playlist, in the order of the site.
	NewTracks []hardstyle.Track
}

// webhook is the body Discord expects. It carries no username, so Discord uses the name the webhook has in the
// channel settings.
type webhook struct {
	Embeds []embed `json:"embeds"`
	// No pings: a track title that looks like a mention must not ring anybody.
	AllowedMentions allowedMentions `json:"allowed_mentions"`
}

type allowedMentions struct {
	Parse []string `json:"parse"`
}

type embed struct {
	Title       string  `json:"title,omitempty"`
	URL         string  `json:"url,omitempty"`
	Description string  `json:"description,omitempty"`
	Color       int     `json:"color,omitempty"`
	Fields      []field `json:"fields,omitempty"`
	Footer      *footer `json:"footer,omitempty"`
	Timestamp   string  `json:"timestamp,omitempty"`
}

type field struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type footer struct {
	Text string `json:"text"`
}

// SendRun sends the summary of a finished run. An empty webhookURL does nothing.
func SendRun(ctx context.Context, hc *http.Client, webhookURL string, s RunSummary) error {
	return send(ctx, hc, webhookURL, runEmbed(s))
}

// SendFailure sends a short note about a run that failed. An empty webhookURL does nothing.
func SendFailure(ctx context.Context, hc *http.Client, webhookURL string, at time.Time, err error) error {
	return send(ctx, hc, webhookURL, failureEmbed(at, err))
}

// Changed reports whether the run wrote anything to the playlist.
func (s RunSummary) Changed() bool { return s.Added > 0 || s.Removed > 0 }

func runEmbed(s RunSummary) embed {
	e := embed{
		Title:     i18n.T("Playlist aktualisiert", "Playlist updated"),
		Color:     colorChanged,
		Timestamp: s.At.UTC().Format(time.RFC3339),
		Footer:    &footer{Text: "harderstyles-radar"},
	}
	if !s.Changed() {
		e.Title, e.Color = i18n.T("Nichts Neues", "No new tracks"), colorQuiet
	}
	if s.PlaylistID != "" {
		e.URL = "https://open.spotify.com/playlist/" + s.PlaylistID
	}

	var b strings.Builder
	if s.Changed() {
		b.WriteString(fmt.Sprintf(i18n.T("**%d neu**, **%d entfernt** · %d Tracks in der Playlist",
			"**%d new**, **%d removed** · %d tracks in the playlist"), s.Added, s.Removed, s.After))
	} else {
		b.WriteString(fmt.Sprintf(i18n.T("Keine neuen Tracks auf hardstyle.com · %d Tracks in der Playlist",
			"No new tracks on hardstyle.com · %d tracks in the playlist"), s.After))
	}
	if len(s.NewTracks) > 0 {
		b.WriteString("\n\n")
		for i, t := range s.NewTracks {
			if i == maxListed {
				b.WriteString(fmt.Sprintf(i18n.T("… und %d weitere", "… and %d more"), len(s.NewTracks)-maxListed))
				break
			}
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString("• " + trackLine(t))
		}
	}
	if e.URL != "" {
		b.WriteString("\n\n[" + i18n.T("Zur Playlist", "Open playlist") + "](" + e.URL + ")")
	}
	e.Description = cut(b.String(), 4000)

	e.Fields = []field{
		{Name: i18n.T("Neu", "New"), Value: strconv.Itoa(s.Added), Inline: true},
		{Name: i18n.T("Entfernt", "Removed"), Value: strconv.Itoa(s.Removed), Inline: true},
		{Name: i18n.T("Bei Spotify", "On Spotify"), Value: strconv.Itoa(s.Matched), Inline: true},
		{Name: i18n.T("Fehlen", "Missing"), Value: strconv.Itoa(s.Missing), Inline: true},
		{Name: i18n.T("Warten", "Waiting"), Value: strconv.Itoa(s.Deferred), Inline: true},
		{Name: i18n.T("Anfragen", "Requests"), Value: strconv.Itoa(s.Requests), Inline: true},
	}
	return e
}

func failureEmbed(at time.Time, err error) embed {
	text := i18n.T("unbekannter Fehler", "unknown error")
	if err != nil {
		text = err.Error()
	}
	return embed{
		Title:       i18n.T("Lauf fehlgeschlagen", "Run failed"),
		Description: cut(text, 3500),
		Color:       colorError,
		Footer:      &footer{Text: "harderstyles-radar"},
		Timestamp:   at.UTC().Format(time.RFC3339),
	}
}

// trackLine names a release: artist and title, in the way the site shows it.
func trackLine(t hardstyle.Track) string {
	line := t.Title
	if t.Version != "" {
		line += " (" + t.Version + ")"
	}
	if t.Artist != "" {
		line = t.Artist + " – " + line
	}
	return escape(line)
}

// markdown escapes the characters Discord would read as formatting.
var markdown = strings.NewReplacer(
	`\`, `\\`, `*`, `\*`, `_`, `\_`, "`", "\\`", `~`, `\~`, `|`, `\|`, `>`, `\>`,
)

func escape(text string) string { return markdown.Replace(text) }

// cut shortens a text to n characters, Discord's limits are counted in characters.
func cut(text string, n int) string {
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	return string(r[:n-1]) + "…"
}

func send(ctx context.Context, hc *http.Client, webhookURL string, e embed) error {
	if strings.TrimSpace(webhookURL) == "" {
		return nil
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	body, err := json.Marshal(webhook{
		Embeds:          []embed{e},
		AllowedMentions: allowedMentions{Parse: []string{}},
	})
	if err != nil {
		return fmt.Errorf(i18n.T("Discord-Nachricht bauen: %w", "building the Discord message: %w"), err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return hideURL(err, webhookURL)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return hideURL(err, webhookURL)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return fmt.Errorf(i18n.T("Discord antwortet mit %s: %s", "Discord answers with %s: %s"),
			resp.Status, cut(strings.TrimSpace(string(msg)), 300))
	}
	return nil
}

// hideURL keeps the secret address of the webhook out of the log: a transport error carries the full URL.
func hideURL(err error, webhookURL string) error {
	return errors.New(strings.ReplaceAll(err.Error(), webhookURL, "DISCORD_WEBHOOK_URL"))
}
