package match

import (
	"hardstylesync/internal/hardstyle"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var (
	// "(Extended Mix)" etc. say nothing about which song it is, remixes do.
	noiseWords  = regexp.MustCompile(`\b(original|extended|radio|club|edit|mix|version)\b`)
	remixWords  = regexp.MustCompile(`\b(remix|bootleg|rework|vip|flip|mashup)\b`)
	artistSplit = regexp.MustCompile(`(?i)\s*(?:,|&|\band\b|\bfeat\.?|\bfeaturing\b|\bft\.?|\bvs\.?|\bx\b|\bpres\.?)\s*`)
	// versionWords tell that the rest of a title is another version of the same song.
	versionWords = regexp.MustCompile(`\b(mix|remix|edit|version|rework|bootleg|vip|flip|dub|instrumental|acoustic|live|radio|extended|original|club|cut|remaster|remastered)\b`)
	// articles are words one site writes and the other leaves out: "Gabber I'm a G" and "Gabber I'm G".
	articles = map[string]bool{"a": true, "an": true, "the": true}
	// artistJoiners are the words between two artist names; Spotify's search ranks a query badly with them in it.
	artistJoiners = regexp.MustCompile(`(?i)\s*(?:,|&|\+|/|\band\b|\bfeat\.?|\bfeaturing\b|\bft\.?|\bvs\.?|\bpres\.?)\s*`)
)

// letters that do not fall apart into a letter and an accent
var specialLetters = strings.NewReplacer("ø", "o", "Ø", "O", "æ", "ae", "Æ", "AE", "œ", "oe", "Œ", "OE", "ß", "ss",
	"đ", "d", "Đ", "D", "ð", "d", "Ð", "D", "ł", "l", "Ł", "L", "ı", "i", "þ", "th", "Þ", "Th", "ħ", "h", "Ħ", "H")

// fold takes the accents and special letters out of a text: Juliëx and GRIVØ become Juliex and GRIVO, so
// the two spellings of a name are the same name.
func Fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(specialLetters.Replace(s)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormTitle makes "Drugs (Crypsis Remix)" and "Drugs - Crypsis Remix" comparable
// and ignores "Extended Mix" / "Radio Edit" style suffixes.
func NormTitle(s string) string {
	s = strings.ToLower(Fold(s))
	// Keep the remix name, drop the pure format words.
	words := strings.Fields(mapNonAlnum(s))
	var kept []string
	for _, w := range words {
		if noiseWords.MatchString(w) && !remixWords.MatchString(w) {
			continue
		}
		kept = append(kept, w)
	}
	return strings.Join(dropArticles(kept), " ")
}

// dropArticles takes "a", "an" and "the" out of a title, as long as a word of the song is left: the two sites
// do not always write the article ("Gabber I'm a G" on Spotify, "Gabber I'm G" on hardstyle.com).
func dropArticles(words []string) []string {
	named := slices.ContainsFunc(words, func(w string) bool { return !articles[w] })
	if !named {
		return words
	}
	out := make([]string, 0, len(words))
	for _, w := range words {
		if !articles[w] {
			out = append(out, w)
		}
	}
	return out
}

// SearchArtists turns the artist line of a release into the words of a search. The joiners between the names
// ("and", "&", ",") spoil what Spotify's search returns: "Gabber I'm G Double Trouble and Dedicated" finds
// nothing, "Gabber I'm G Double Trouble Dedicated" the right track. Names are kept whole, "X-Pander" stays.
func SearchArtists(s string) string {
	return strings.Join(strings.Fields(artistJoiners.ReplaceAllString(s, " ")), " ")
}

func NormName(s string) string {
	return strings.Join(strings.Fields(mapNonAlnum(strings.ToLower(Fold(s)))), " ")
}

func mapNonAlnum(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
}

// SplitArtists splits "Act of Rage, Spitnoise and Nolz" into single artist names.
func SplitArtists(s string) []string {
	var out []string
	for _, p := range artistSplit.Split(s, -1) {
		if n := NormName(p); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// editDistance is the number of letters to change, add or remove to turn a into b.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// NamesClose tells whether two artist names (already normalized with NormName) are the same name: equal, equal
// without the spaces ("Jose Del Barrio" and "JoseDelBarrio"), one the beginning of the other ("Act of Rage" and
// "Act of Rage Official"), or one letter apart.
func NamesClose(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b || strings.ReplaceAll(a, " ", "") == strings.ReplaceAll(b, " ", "") {
		return true
	}
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	if len([]rune(short)) < 5 {
		return false
	}
	if strings.HasPrefix(long, short) && len(short)*2 >= len(long) {
		return true
	}
	return editDistance(a, b) <= 1
}

// Artists tells whether the artists of the site and of a Spotify track share an artist.
func Artists(siteArtist string, spotifyArtists []string) bool {
	var have []string
	for _, a := range spotifyArtists {
		have = append(have, NormName(a))
	}
	for _, s := range SplitArtists(siteArtist) {
		for _, h := range have {
			if NamesClose(s, h) {
				return true
			}
		}
	}
	// A name can be split wrongly ("x" inside names): fall back to the whole name as whole words in the artists
	// of Spotify. Short names are too common for that.
	whole := NormName(siteArtist)
	return len(whole) >= 5 && strings.Contains(" "+strings.Join(have, " ")+" ", " "+whole+" ")
}

// Kind says how well the title of a Spotify track fits a release.
type Kind int

const (
	None Kind = iota
	// Exact: the same title, with the version the site names (Everything Is Looking Good + Fiesta Mix).
	Exact
	// Base: the title without the version the site names.
	Base
	// Version: another version of the song, a remix, that the site does not name.
	Version
)

// Title compares the title of a release (with its version line) with the title of a Spotify track.
func Title(site hardstyle.Track, spotifyName string) Kind {
	full := NormTitle(site.Title + " " + site.Version)
	base := NormTitle(site.Title)
	c := NormTitle(spotifyName)
	switch {
	case c == "" || base == "":
		return None
	case c == full || (len(full) >= 10 && editDistance(c, full) <= 1):
		return Exact
	case full != base && c == base:
		return Base
	}
	// "Song - Some Remix": the title starts with the title of the site and what follows is a version.
	if strings.HasPrefix(c, base+" ") {
		rest := strings.TrimPrefix(strings.ToLower(Fold(spotifyName)), strings.ToLower(Fold(site.Title)))
		if versionWords.MatchString(rest) {
			return Version
		}
	}
	return None
}

// Matches reports whether a Spotify track is the release of the site: the title fits and an artist is the same.
func Matches(site hardstyle.Track, name string, artists []string) bool {
	return Title(site, name) != None && Artists(site.Artist, artists)
}

var (
	// featParts are the guest artists in a title: "Song (feat. MC X)", "Song ft. MC X - Extended Mix". They say
	// nothing about the version of the song.
	featParts = []*regexp.Regexp{
		regexp.MustCompile(`[(\[]\s*(?:feat|ft|featuring|with)\b[^)\]]*[)\]]`),
		regexp.MustCompile(`\s(?:feat\.?|ft\.?|featuring)\s[^(\[]*?(?:\s-\s|$)`),
	}
	// formatWords name the cut of a recording, not another version: "Extended Mix", "Radio Edit", "Original Mix".
	formatWords = map[string]bool{"original": true, "extended": true, "radio": true, "club": true, "edit": true,
		"mix": true, "version": true, "cut": true, "remaster": true, "remastered": true}
	// formats are the format words that tell two cuts of the same song apart.
	formats = map[string]bool{"extended": true, "radio": true, "club": true}
	// identityNoise are the words that do not change which recording a title names: "Song - Original Mix" is
	// "Song", "Song (Radio Version)" is "Song - Radio Edit".
	identityNoise = map[string]bool{"original": true, "mix": true, "edit": true, "version": true}
)

// withoutFeat takes the guest artists out of a title, in lower case and without accents.
func withoutFeat(title string) string {
	s := strings.ToLower(Fold(title))
	for _, re := range featParts {
		s = re.ReplaceAllString(s, " - ")
	}
	return s
}

// Tags are what a title says beyond the song itself.
type Tags struct {
	// Names are the words that name another version of the song, sorted: the remixer and "remix" ("crypsis
	// remix"), "vip", "instrumental", the name of a mix ("fiesta"). Format words are not among them.
	Names []string
	// Format is the cut the title names: "extended", "radio", "club", or "" when it names none.
	Format string
}

// TitleTags returns what a title (the song and its version: "Drugs - Crypsis Remix", or the title of the site
// with its version line) says beyond the song, whose title is song. Words of the song are taken out once each,
// wherever they stand, so "Gabber I'm A G" against "Gabber I'm G" leaves nothing but the article, which goes too.
func TitleTags(song, title string) Tags {
	rest := strings.Fields(NormName(withoutFeat(title)))
	for _, w := range strings.Fields(NormName(song)) {
		if i := slices.Index(rest, w); i >= 0 {
			rest = slices.Delete(rest, i, i+1)
		}
	}
	var tags Tags
	for _, w := range rest {
		switch {
		case formats[w] && tags.Format == "":
			tags.Format = w
		case formatWords[w] || articles[w]:
		default:
			tags.Names = append(tags.Names, w)
		}
	}
	slices.Sort(tags.Names)
	tags.Names = slices.Compact(tags.Names)
	return tags
}

// SameNames tells whether two tags name the same version: both no remix name, or the same words.
func (t Tags) SameNames(o Tags) bool { return slices.Equal(t.Names, o.Names) }

// Identity is the key of the recording a Spotify title names: the title without guest artists and without the
// words that do not change the recording. The same recording on a single and on an album has the same key;
// "Song - Extended Mix" and "Song - Radio Edit" have different ones.
func Identity(title string) string {
	words := strings.Fields(NormName(withoutFeat(title)))
	kept := words[:0]
	for _, w := range words {
		if !identityNoise[w] {
			kept = append(kept, w)
		}
	}
	return strings.Join(dropArticles(kept), " ")
}

// GraceDays is how far the dates of a track whose title (with its version) and artist fit may be apart and
// it is still taken: hardstyle.com and Spotify often name different days for the same release (a pre-order, a
// shop that lists it later, a label that moves the date). More than a few weeks is another release, or an old
// single of the same name, and stays out. Such a track is marked, so the screen says its date is off.
const GraceDays = 45

// Date compares the release date on hardstyle.com with the one of a Spotify track. Spotify knows a day, a
// month or only a year, and the match must be near the date of hardstyle.com. When one of the dates is unknown
// there is nothing to compare and the track passes. The second result is the distance in days.
func Date(siteDate, spotifyDate, precision string, toleranceDays int) (bool, int) {
	site, err := time.Parse("2006-01-02", siteDate)
	if err != nil || spotifyDate == "" {
		return true, 0
	}
	tolerance := time.Duration(toleranceDays) * 24 * time.Hour
	days := func(d time.Duration) int { return int(d.Round(24*time.Hour) / (24 * time.Hour)) }
	abs := func(d time.Duration) time.Duration {
		if d < 0 {
			return -d
		}
		return d
	}
	switch precision {
	case "year":
		year, err := strconv.Atoi(spotifyDate)
		if err != nil {
			return true, 0
		}
		return site.Year() == year, 0
	case "month":
		start, err := time.Parse("2006-01", spotifyDate)
		if err != nil {
			return true, 0
		}
		end := start.AddDate(0, 1, -1)
		switch {
		case site.Before(start):
			return start.Sub(site) <= tolerance, days(start.Sub(site))
		case site.After(end):
			return site.Sub(end) <= tolerance, days(site.Sub(end))
		}
		return true, 0
	}
	spotify, err := time.Parse("2006-01-02", spotifyDate)
	if err != nil {
		return true, 0
	}
	diff := abs(site.Sub(spotify))
	return diff <= tolerance, days(diff)
}
