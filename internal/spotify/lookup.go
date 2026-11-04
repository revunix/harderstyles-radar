package spotify

import (
	"context"
	"fmt"
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/match"
	"net/http"
	"net/url"
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
	// ReasonUncertain: two different tracks fit almost equally well. None is taken; a correction decides
	// (corrections.txt), and the release is looked up again when it is due, like a missing one.
	ReasonUncertain = "uncertain"
)

// MatchOptions steer the matching of a release.
type MatchOptions struct {
	Tolerance int // DATE_TOLERANCE_DAYS
	// ArtistIDs returns the Spotify artist ids learned for a site artist name (match.NormName of one name of
	// match.SplitArtists); nil when none is known. nil func: no aliases.
	ArtistIDs func(name string) []string
	// Sample asks for the title search even when the ISRC finds the track: its answer goes into
	// Lookup.Searched, so the evaluation can check the title matching against the ISRC's answer.
	Sample bool
	// MinScore is the score the best track needs (see Score); MinMargin how far it must be ahead of a different
	// recording. 0 takes DefaultMinScore and DefaultMinMargin; the evaluation tries other values.
	MinScore, MinMargin float64
	// Since is the oldest day (yyyy-mm-dd) a Spotify track may have when the release's own date is unknown
	// ("" = no such limit). The window of the run knows how old its oldest release is; a track from years ago
	// must not enter a playlist of the newest releases just because the date of the release could not be read.
	Since string
}

// maxVersions is the largest number of Spotify tracks (versions of one song) a release brings into the playlist.
const maxVersions = 4

// Lookup is the answer of FindTrack.
type Lookup struct {
	URIs        []string // the best match first, then other versions (remixes) of the song; empty if there is none
	SpotifyDate string   // the release date of the best match; with ReasonDate that of the track that was too far off
	DaysOff     int      // the best match fits in title and artist, but its date is this many days off
	Reason      string   // why there is no match: ReasonNotFound, ReasonDate or ReasonUncertain
	// Candidates are all tracks the searches found whose title and artist fit, whatever their date: the tracks
	// of the release on a single and on an album. The caller tells from them which album a group of releases
	// has in common.
	Candidates []Candidate
	// Searched are the tracks the title searches found (not the ISRC search), best score first, at most
	// maxSearched. The cache keeps them, so the evaluation can replay the matching offline (Decide).
	Searched []Track
	// Sampled: the ISRC found the track and the title search ran as well, only for Searched (MatchOptions.Sample).
	Sampled bool
	// ByISRC: the URIs come from the ISRC search, as sure as a match gets.
	ByISRC bool
}

// Candidate is a Spotify track whose title and artist fit a release.
type Candidate struct {
	Track Track
	Kind  match.Kind
	Diff  int     // days between the dates
	Score float64 // how well it fits, higher is better; see Score
}

// FindTrack returns the Spotify tracks of a release. It tries, as long as nothing is found:
//
//  1. the ISRC from the page on hardstyle.com, which names exactly one recording;
//  2. the title and the artist, scored by Decide: the same title (with the version line of the site), an artist
//     that is the same (accents and special letters do not matter: Juliëx is Juliex), the length, and a release
//     date near the date on hardstyle.com. A track whose title and artist fit is taken even if its date is up to
//     match.GraceDays off: the two sites often name different days for the same release. It is marked as such.
//     Remixes ("Song - Some Remix") of the same song come along.
//
// Each search is one request; a hit in the first one ends the lookup. With o.Sample the title searches run
// after a hit of the ISRC as well, for Lookup.Searched only.
func (s *Client) FindTrack(ctx context.Context, t hardstyle.Track, d hardstyle.Details, o MatchOptions) (Lookup, error) {
	if d.ISRC != "" {
		found, err := s.Search(ctx, "isrc:"+d.ISRC)
		if err != nil {
			return Lookup{}, err
		}
		look := byISRC(found, d, o)
		if len(look.URIs) > 0 {
			for _, f := range found {
				_, diff := dateFits(d.Date, f.ReleaseDate, f.DatePrecision, o)
				look.Candidates = append(look.Candidates, Candidate{Track: f, Kind: match.Exact, Diff: diff})
			}
			look.ByISRC = true
			if !o.Sample {
				return look, nil
			}
			byTitle, err := s.findByTitle(ctx, t, d, o)
			if err != nil {
				return Lookup{}, err
			}
			look.Searched, look.Sampled = byTitle.Searched, true
			return look, nil
		}
		// The ISRC names only recordings that came out at another time (a re-master, a compilation): the title
		// search decides, and when it finds nothing the answer still says that a track exists, with another date.
		byTitle, err := s.findByTitle(ctx, t, d, o)
		if err != nil {
			return Lookup{}, err
		}
		if len(byTitle.URIs) == 0 && byTitle.Reason == ReasonNotFound && look.Reason == ReasonDate {
			byTitle.Reason, byTitle.SpotifyDate = ReasonDate, look.SpotifyDate
		}
		return byTitle, nil
	}
	return s.findByTitle(ctx, t, d, o)
}

// findByTitle searches for the title and the artist of a release. The second query runs only when Decide does
// not take a track from the first; Decide always sees all that both found.
func (s *Client) findByTitle(ctx context.Context, t hardstyle.Track, d hardstyle.Details, o MatchOptions) (Lookup, error) {
	queries := []string{t.Title + " " + match.SearchArtists(t.Artist)}
	if first := match.SplitArtists(t.Artist); len(first) > 0 {
		// Fielded query second, it is more precise for generic titles.
		queries = append(queries, fmt.Sprintf("track:%s artist:%s", t.Title, first[0]))
	}
	var found []Track
	var look Lookup
	for _, q := range queries {
		res, err := s.Search(ctx, q)
		if err != nil {
			return Lookup{}, err
		}
		found = append(found, res...)
		if look = Decide(t, d, found, o); len(look.URIs) > 0 {
			break
		}
	}
	return look, nil
}

// byISRC takes the answer of a search for an ISRC: the tracks have that recording. The same recording comes out
// again years later on a re-master or a compilation, so only a track whose release date fits the date of the
// release counts; an old track must not enter a playlist of the newest releases. Within the tolerance the date
// costs nothing, up to match.GraceDays the track is taken and marked (DaysOff). One track per title is taken,
// at most two, the nearest date first.
func byISRC(found []Track, d hardstyle.Details, o MatchOptions) Lookup {
	var good, near []Candidate
	nearest, nearestDiff := "", 1<<31-1
	for _, f := range found {
		ok, diff := dateFits(d.Date, f.ReleaseDate, f.DatePrecision, o)
		if diff < nearestDiff {
			nearest, nearestDiff = f.ReleaseDate, diff
		}
		c := Candidate{Track: f, Kind: match.Exact, Diff: diff}
		switch {
		case ok:
			good = append(good, c)
		case diff > o.Tolerance && diff <= match.GraceDays:
			near = append(near, c)
		}
	}
	if len(good) == 0 && len(near) == 0 {
		if len(found) == 0 {
			return Lookup{Reason: ReasonNotFound}
		}
		return Lookup{Reason: ReasonDate, SpotifyDate: nearest}
	}
	sort.SliceStable(good, func(i, j int) bool { return good[i].Diff < good[j].Diff })
	sort.SliceStable(near, func(i, j int) bool { return near[i].Diff < near[j].Diff })
	pick := append(good, near...)
	var look Lookup
	titles := map[string]bool{}
	for _, c := range pick {
		key := match.NormTitle(c.Track.Name)
		if titles[key] || len(look.URIs) >= 2 {
			continue
		}
		titles[key] = true
		look.URIs = append(look.URIs, c.Track.URI)
		if look.SpotifyDate == "" {
			look.SpotifyDate = c.Track.ReleaseDate
		}
		if c.Diff > o.Tolerance && (look.DaysOff == 0 || c.Diff < look.DaysOff) {
			look.DaysOff = c.Diff
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
