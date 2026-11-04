package spotify

import (
	"hardstylesync/internal/hardstyle"
	"hardstylesync/internal/match"
	"slices"
	"sort"
)

// The weights of the score of a Spotify track against a release, all in one place. A track whose title and
// artist fit and that nothing speaks against scores about 4; DefaultMinScore lies where one strong piece of
// evidence against it (a length far off together with a cut the site does not name) or several weak ones push
// it below. The evaluation (internal/eval) replays the matching on what the searches found and shows where the
// thresholds would land; change them there first.
const (
	// The title: the same title with the version of the site is the strongest sign. Without the version the site
	// names (Base) it is likely the release, as Spotify often leaves a mix name out. Another version (a remix)
	// alone is not the release: it comes along with the release, but on its own it stays below DefaultMinScore.
	weightExact   = 3.0
	weightBase    = 2.0
	weightVersion = 0.5
	// weightArtist is given in full when the artists on both sides are the same set, in part when only some
	// of them are (a guest the other side leaves out).
	weightArtist = 1.0
	// The length: hardstyle.com names it to the second, as Spotify does. Within nearSeconds it is the same
	// recording, which is the best evidence there is besides the ISRC; farSeconds or more apart it is another
	// cut (an Extended Mix of 5:30 against a Radio Edit of 3:10). Between the two the score goes down evenly.
	weightLengthNear = 1.5
	weightLengthFar  = 2.5
	nearSeconds      = 5
	farSeconds       = 30
	// clearlyOtherSeconds: two tracks of the same title this far apart in length are different recordings.
	clearlyOtherSeconds = 10
	// weightNames is taken off when the remix names differ: one side names a remix ("Crypsis Remix", "VIP") the
	// other does not, or they name different ones. An exact title already agrees on them, so it applies to the
	// other kinds only.
	weightNames = 1.0
	// The cut: both sides name the same (Extended and Extended) is a small plus, different ones (Extended
	// against Radio) a clear minus. When one side names none, nothing is known.
	weightFormatSame  = 0.25
	weightFormatClash = 1.0
	// The date: within the tolerance it costs nothing, up to match.GraceDays it costs up to weightDate (the
	// sites often name different days for the same release), beyond it the track is not the release at all.
	// weightDateReject only ranks such tracks low in Lookup.Searched; Decide never takes them.
	weightDate       = 1.0
	weightDateReject = 5.0
	// The label is not compared: Spotify no longer names the label of an album (removed in the Web API changes
	// of February 2026), and the album name of a track is not a label.

	// DefaultMinScore is the score the best track needs to be taken (MatchOptions.MinScore 0).
	DefaultMinScore = 1.0
	// DefaultMinMargin is how far the best track must be ahead of a different recording that fits as well
	// (MatchOptions.MinMargin 0). Closer than that, neither is taken (ReasonUncertain).
	DefaultMinMargin = 0.75
)

// maxSearched is the number of tracks of the title searches a Lookup keeps for the evaluation.
const maxSearched = 8

// scored is a track with its score and the facts Decide needs besides it.
type scored struct {
	Candidate
	fits     bool // the title fits and an artist is the same
	dateOK   bool // the date is within the tolerance or the grace
	nearLen  bool // the length is within nearSeconds of the release
	identity string
	pos      int // where the search answers named it
}

// Score tells how well a Spotify track fits a release: Kind is how the titles compare, Diff the days between
// the dates (as match.Date counts them), Score the sum of the weights above. A track whose title or artist does
// not fit still gets a score, so the tracks of a search can be ranked; Decide does not take it.
func Score(t hardstyle.Track, d hardstyle.Details, c Track, o MatchOptions) Candidate {
	return score(t, d, c, o).Candidate
}

func score(t hardstyle.Track, d hardstyle.Details, c Track, o MatchOptions) scored {
	s := scored{Candidate: Candidate{Track: c, Kind: match.Title(t, c.Name)}, identity: match.Identity(c.Name)}
	switch s.Kind {
	case match.Exact:
		s.Score += weightExact
	case match.Base:
		s.Score += weightBase
	case match.Version:
		s.Score += weightVersion
	}

	share, fit := artistShare(t.Artist, c, o.ArtistIDs)
	s.Score += weightArtist * share
	s.fits = s.Kind != match.None && fit

	site := d.Duration
	if site <= 0 {
		site = t.Duration
	}
	var lengthScore float64
	lengthScore, s.nearLen = lengthFit(site, c.Duration)
	s.Score += lengthScore

	siteTags := match.TitleTags(t.Title, t.Title+" "+t.Version)
	tags := match.TitleTags(t.Title, c.Name)
	if s.Kind != match.Exact && !siteTags.SameNames(tags) {
		s.Score -= weightNames
	}
	switch {
	case siteTags.Format == "" || tags.Format == "":
	case siteTags.Format == tags.Format:
		s.Score += weightFormatSame
	default:
		s.Score -= weightFormatClash
	}

	ok, diff := match.Date(d.Date, c.ReleaseDate, c.DatePrecision, o.Tolerance)
	s.Diff = diff
	s.dateOK = ok || (diff > o.Tolerance && diff <= match.GraceDays)
	switch {
	case !s.dateOK:
		s.Score -= weightDateReject
	case !ok:
		if span := match.GraceDays - o.Tolerance; span > 0 {
			s.Score -= weightDate * float64(diff-o.Tolerance) / float64(span)
		}
	}
	return s
}

// artistShare compares the artists of the site with those of a Spotify track as two sets: the share of the
// site's artists found among Spotify's and the other way round, averaged. A name fits by the rules of
// match.NamesClose, or when ids knows the Spotify id of the artist for the site's name. fit tells whether an
// artist is the same at all; match.Artists also knows names split wrongly, which count as half.
func artistShare(siteArtist string, c Track, ids func(string) []string) (share float64, fit bool) {
	site := match.SplitArtists(siteArtist)
	if len(site) == 0 || len(c.Artists) == 0 {
		return 0, false
	}
	have := make([]string, len(c.Artists))
	for i, a := range c.Artists {
		have[i] = match.NormName(a)
	}
	found := make([]bool, len(have))
	siteFound := 0
	for _, s := range site {
		var known []string
		if ids != nil {
			known = ids(s)
		}
		hit := false
		for i, h := range have {
			id := ""
			if i < len(c.ArtistIDs) {
				id = c.ArtistIDs[i]
			}
			if match.NamesClose(s, h) || (id != "" && slices.Contains(known, id)) {
				hit, found[i] = true, true
			}
		}
		if hit {
			siteFound++
		}
	}
	spotifyFound := 0
	for _, f := range found {
		if f {
			spotifyFound++
		}
	}
	if siteFound > 0 {
		return (float64(siteFound)/float64(len(site)) + float64(spotifyFound)/float64(len(have))) / 2, true
	}
	if match.Artists(siteArtist, c.Artists) {
		return 0.5, true
	}
	return 0, false
}

// lengthFit scores the lengths of the release and of a track in seconds; 0 is unknown and says nothing.
func lengthFit(site, track int) (float64, bool) {
	if site <= 0 || track <= 0 {
		return 0, false
	}
	diff := abs(site - track)
	switch {
	case diff <= nearSeconds:
		return weightLengthNear, true
	case diff >= farSeconds:
		return -weightLengthFar, false
	}
	part := float64(diff-nearSeconds) / float64(farSeconds-nearSeconds)
	return weightLengthNear - part*(weightLengthNear+weightLengthFar), false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// kindRank orders the kinds of title for a tie: the exact title first, a title that does not fit last.
func kindRank(k match.Kind) int {
	if k == match.None {
		return 4
	}
	return int(k)
}

// sameRecording tells whether two tracks are the same recording as far as their titles and lengths tell: the
// same title (match.Identity) and lengths that are not clearly apart. The same recording on a single and on an
// album is one; an Extended Mix and a Radio Edit of a song are two.
func sameRecording(a, b scored) bool {
	if a.identity != b.identity {
		return false
	}
	return a.Track.Duration <= 0 || b.Track.Duration <= 0 || abs(a.Track.Duration-b.Track.Duration) <= clearlyOtherSeconds
}

// clearlyPreferred tells whether the best track wins over another one for a reason besides the score: a better
// kind of title (the exact title against the title without the version) or a length that fits where the
// other's does not.
func clearlyPreferred(best, other scored) bool {
	return kindRank(best.Kind) < kindRank(other.Kind) || (best.nearLen && !other.nearLen)
}

// Decide picks the Spotify tracks of a release from what the title searches found, without a request. Every
// track is scored (Score); the tracks whose title and artist fit are the candidates, and the best of those within
// the date's grace is taken when it scores at least MinScore. Other versions of the song (remixes) come along,
// up to maxVersions in all. When a different recording fits almost as well (within MinMargin) and nothing else
// prefers the best, none is taken: ReasonUncertain. A title and artist that fit only with a date beyond the
// grace give ReasonDate, nothing at all ReasonNotFound.
func Decide(t hardstyle.Track, d hardstyle.Details, found []Track, o MatchOptions) Lookup {
	minScore, minMargin := o.MinScore, o.MinMargin
	if minScore == 0 {
		minScore = DefaultMinScore
	}
	if minMargin == 0 {
		minMargin = DefaultMinMargin
	}

	var all []scored
	seen := map[string]bool{}
	for _, f := range found {
		if seen[f.URI] {
			continue
		}
		seen[f.URI] = true
		s := score(t, d, f, o)
		s.pos = len(all)
		all = append(all, s)
	}

	var look Lookup
	// Candidates keep the order of the search answers, as the album check expects.
	for _, s := range all {
		if s.fits {
			look.Candidates = append(look.Candidates, s.Candidate)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		switch {
		case a.Score != b.Score:
			return a.Score > b.Score
		case a.Kind != b.Kind:
			return kindRank(a.Kind) < kindRank(b.Kind)
		case a.Diff != b.Diff:
			return a.Diff < b.Diff
		}
		return a.pos < b.pos
	})
	for _, s := range all[:min(len(all), maxSearched)] {
		look.Searched = append(look.Searched, s.Track)
	}

	var takeable []scored
	for _, s := range all {
		if s.fits && s.dateOK {
			takeable = append(takeable, s)
		}
	}
	if len(takeable) == 0 || takeable[0].Score < minScore {
		return withoutMatch(look, all)
	}
	best := takeable[0]
	if best.Kind != match.Version {
		for _, s := range takeable[1:] {
			if best.Score-s.Score >= minMargin {
				break // the others are farther behind
			}
			if s.Kind != match.Version && !sameRecording(best, s) && !clearlyPreferred(best, s) {
				look.Reason = ReasonUncertain
				return look
			}
		}
	}

	look.URIs = []string{best.Track.URI}
	look.SpotifyDate = best.Track.ReleaseDate
	if best.Diff > o.Tolerance {
		look.DaysOff = best.Diff
	}
	taken := []scored{best}
	for _, s := range takeable[1:] {
		if len(look.URIs) >= maxVersions {
			break
		}
		if s.Kind != match.Version || slices.ContainsFunc(taken, func(o scored) bool { return sameRecording(o, s) }) {
			continue
		}
		taken = append(taken, s)
		look.URIs = append(look.URIs, s.Track.URI)
	}
	return look
}

// withoutMatch is the answer when no track is taken: ReasonDate with the nearest date when the title and an
// artist fit, only the date not; ReasonNotFound otherwise. Remixes alone do not make it a date mismatch.
func withoutMatch(look Lookup, all []scored) Lookup {
	var dated []scored
	for _, s := range all {
		if s.fits && !s.dateOK && s.Kind != match.Version {
			dated = append(dated, s)
		}
	}
	if len(dated) == 0 {
		look.Reason = ReasonNotFound
		return look
	}
	sort.SliceStable(dated, func(i, j int) bool {
		if dated[i].Diff != dated[j].Diff {
			return dated[i].Diff < dated[j].Diff
		}
		return dated[i].pos < dated[j].pos
	})
	look.Reason, look.SpotifyDate = ReasonDate, dated[0].Track.ReleaseDate
	return look
}
