package spotify

import (
	"context"
	"fmt"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/match"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The reasons why a release has no Spotify track. They are saved in the cache as they are; the text for the
// screen is made from them in the language of the moment.
const (
	ReasonNotFound = "notfound"
	ReasonDate     = "date" // a track of that name exists, but with another release date
)

// maxVersions is the largest number of Spotify tracks (versions of one song) a release brings into the playlist.
const maxVersions = 4

// Lookup is the answer of FindTrack.
type Lookup struct {
	URIs        []string // the best match first, then other versions (remixes) of the song; empty if there is none
	SpotifyDate string   // the release date of the best match; with ReasonDate that of the track that was too far off
	DaysOff     int      // the best match fits in title and artist, but its date is this many days off
	Reason      string   // why there is no match: ReasonNotFound or ReasonDate
	// Candidates are all tracks the searches found whose title and artist fit, whatever their date: the tracks
	// of the release on a single and on an album. The caller tells from them which album a group of releases
	// has in common.
	Candidates []Candidate
}

// Candidate is a Spotify track whose title and artist fit a release.
type Candidate struct {
	Track Track
	Kind  match.Kind
	Diff  int // days between the dates
}

// FindTrack returns the Spotify tracks of a release. It tries, as long as nothing is found:
//
//  1. the ISRC from the page on hardstyle.com, which names exactly one recording;
//  2. the title and the artist: the same title (with the version line of the site), an artist that is the same
//     (accents and special letters do not matter: Juliëx is Juliex), and a release date within tolerance days of
//     the date on hardstyle.com. A track whose title and artist fit is taken even if its date is up to
//     match.GraceDays off: the two sites often name different days for the same release. It is marked as such.
//     Remixes ("Song - Some Remix") of the same song come along.
//
// Each search is one request; a hit in the first one ends the lookup.
func (s *Client) FindTrack(ctx context.Context, t hardstyle.Track, d hardstyle.Details, tolerance int) (Lookup, error) {
	if d.ISRC != "" {
		found, err := s.Search(ctx, "isrc:"+d.ISRC)
		if err != nil {
			return Lookup{}, err
		}
		if look := byISRC(found, d.Date); len(look.URIs) > 0 {
			for _, f := range found {
				_, diff := match.Date(d.Date, f.ReleaseDate, f.DatePrecision, 0)
				look.Candidates = append(look.Candidates, Candidate{f, match.Exact, diff})
			}
			return look, nil
		}
	}

	queries := []string{t.Title + " " + match.SearchArtists(t.Artist)}
	if first := match.SplitArtists(t.Artist); len(first) > 0 {
		// Fielded query second, it is more precise for generic titles.
		queries = append(queries, fmt.Sprintf("track:%s artist:%s", t.Title, first[0]))
	}
	var older, all []Candidate
	for _, q := range queries {
		found, err := s.Search(ctx, q)
		if err != nil {
			return Lookup{}, err
		}
		var good []Candidate
		for _, f := range found {
			kind := match.Title(t, f.Name)
			if kind == match.None || !match.Artists(t.Artist, f.Artists) {
				continue
			}
			ok, diff := match.Date(d.Date, f.ReleaseDate, f.DatePrecision, tolerance)
			if !slices.ContainsFunc(all, func(c Candidate) bool { return c.Track.URI == f.URI }) {
				all = append(all, Candidate{f, kind, diff})
			}
			switch {
			case ok:
				good = append(good, Candidate{f, kind, diff})
			case diff > tolerance && diff <= match.GraceDays:
				// Title and artist fit, only the date is off: the same release whose date the two sites do not
				// name the same. It is taken and marked (DaysOff).
				good = append(good, Candidate{f, kind, diff})
			case kind != match.Version:
				older = append(older, Candidate{f, kind, diff})
			}
		}
		if len(good) > 0 {
			look := lookupOf(good, tolerance)
			look.Candidates = all
			return look, nil
		}
	}
	if len(older) > 0 {
		sort.SliceStable(older, func(i, j int) bool { return older[i].Diff < older[j].Diff })
		return Lookup{Reason: ReasonDate, SpotifyDate: older[0].Track.ReleaseDate, Candidates: all}, nil
	}
	return Lookup{Reason: ReasonNotFound, Candidates: all}, nil
}

// lookupOf turns the candidates into the answer: the exact title first, then the other versions, the nearest
// release date first within each. A best match whose date is farther off than the tolerance is marked with the
// days it is off.
func lookupOf(good []Candidate, tolerance int) Lookup {
	sort.SliceStable(good, func(i, j int) bool {
		if good[i].Kind != good[j].Kind {
			return good[i].Kind < good[j].Kind
		}
		return good[i].Diff < good[j].Diff
	})
	look := Lookup{SpotifyDate: good[0].Track.ReleaseDate}
	if good[0].Diff > tolerance {
		look.DaysOff = good[0].Diff
	}
	seen := map[string]bool{}
	for _, c := range good {
		if !seen[c.Track.URI] && len(look.URIs) < maxVersions {
			seen[c.Track.URI] = true
			look.URIs = append(look.URIs, c.Track.URI)
		}
	}
	return look
}

// byISRC takes the answer of a search for an ISRC: the tracks have that recording. The same recording can be on
// a single and on an album; one of each title is taken, the nearest release date first.
func byISRC(found []Track, siteDate string) Lookup {
	var cands []Candidate
	for _, f := range found {
		_, diff := match.Date(siteDate, f.ReleaseDate, f.DatePrecision, 0)
		cands = append(cands, Candidate{Track: f, Diff: diff})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Diff < cands[j].Diff })
	var look Lookup
	titles := map[string]bool{}
	for _, c := range cands {
		key := match.NormTitle(c.Track.Name)
		if titles[key] || len(look.URIs) >= 2 {
			continue
		}
		titles[key] = true
		look.URIs = append(look.URIs, c.Track.URI)
		if look.SpotifyDate == "" {
			look.SpotifyDate = c.Track.ReleaseDate
		}
	}
	return look
}

// PlaylistSnapshotID returns the snapshot id of the playlist. It changes with every change of the playlist, so
// one small request tells whether the remembered tracks are still right.
func (s *Client) PlaylistSnapshotID(ctx context.Context, playlistID string) (string, error) {
	var out struct {
		SnapshotID string `json:"snapshot_id"`
	}
	err := s.do(ctx, http.MethodGet, "/playlists/"+playlistID, url.Values{"fields": {"snapshot_id"}}, nil, &out)
	return out.SnapshotID, err
}

// PlaylistEntries returns all tracks of the playlist, in order. Only works for playlists the user owns.
func (s *Client) PlaylistEntries(ctx context.Context, playlistID string) ([]PlaylistEntry, error) {
	var entries []PlaylistEntry
	for offset := 0; ; offset += 50 {
		var page struct {
			Items []struct {
				AddedAt string `json:"added_at"`
				Item    *struct {
					URI string `json:"uri"`
				} `json:"item"`
				Track *struct { // pre-2026 name, kept in case the API answers with it
					URI string `json:"uri"`
				} `json:"track"`
			} `json:"items"`
			Next string `json:"next"`
		}
		q := url.Values{"limit": {"50"}, "offset": {strconv.Itoa(offset)}}
		if err := s.do(ctx, http.MethodGet, "/playlists/"+playlistID+"/items", q, nil, &page); err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			uri := ""
			switch {
			case it.Item != nil:
				uri = it.Item.URI
			case it.Track != nil:
				uri = it.Track.URI
			}
			if !strings.HasPrefix(uri, "spotify:track:") {
				continue // podcasts, local files, removed tracks
			}
			added, _ := time.Parse(time.RFC3339, it.AddedAt)
			entries = append(entries, PlaylistEntry{URI: uri, AddedAt: added})
		}
		if page.Next == "" {
			return entries, nil
		}
	}
}

// SetDescription changes the description of the playlist.
func (s *Client) SetDescription(ctx context.Context, playlistID, description string) error {
	return s.do(ctx, http.MethodPut, "/playlists/"+playlistID, nil, map[string]any{"description": description}, nil)
}

type snapshotResponse struct {
	SnapshotID string `json:"snapshot_id"`
}

// InsertAtTop puts the tracks at the start of the playlist, in the given order. It returns the new snapshot id.
func (s *Client) InsertAtTop(ctx context.Context, playlistID string, uris []string) (string, error) {
	var snap snapshotResponse
	for i := 0; i < len(uris); i += batchSize {
		body := map[string]any{"uris": uris[i:min(i+batchSize, len(uris))], "position": i}
		if err := s.do(ctx, http.MethodPost, "/playlists/"+playlistID+"/items", nil, body, &snap); err != nil {
			return "", err
		}
	}
	return snap.SnapshotID, nil
}

// RemoveTracks takes the tracks out of the playlist. It returns the new snapshot id.
func (s *Client) RemoveTracks(ctx context.Context, playlistID string, uris []string) (string, error) {
	var snap snapshotResponse
	for i := 0; i < len(uris); i += batchSize {
		end := min(i+batchSize, len(uris))
		items := make([]map[string]string, 0, end-i)
		for _, u := range uris[i:end] {
			items = append(items, map[string]string{"uri": u})
		}
		// Since Feb 2026 the body field is "items" (was "tracks").
		if err := s.do(ctx, http.MethodDelete, "/playlists/"+playlistID+"/items", nil, map[string]any{"items": items}, &snap); err != nil {
			return "", err
		}
	}
	return snap.SnapshotID, nil
}

// ReplaceTracks writes the whole playlist anew, in the given order: the first 100 tracks replace the content,
// the rest is appended. It returns the new snapshot id.
func (s *Client) ReplaceTracks(ctx context.Context, playlistID string, uris []string) (string, error) {
	var snap snapshotResponse
	first := uris[:min(batchSize, len(uris))]
	if err := s.do(ctx, http.MethodPut, "/playlists/"+playlistID+"/items", nil, map[string]any{"uris": first}, &snap); err != nil {
		return "", err
	}
	for i := batchSize; i < len(uris); i += batchSize {
		body := map[string]any{"uris": uris[i:min(i+batchSize, len(uris))]}
		if err := s.do(ctx, http.MethodPost, "/playlists/"+playlistID+"/items", nil, body, &snap); err != nil {
			return "", err
		}
	}
	return snap.SnapshotID, nil
}
