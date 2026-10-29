package event

import (
	"fmt"
	"hardstylesync/internal/hardstyle"
	"time"
)

// Status is the state of one release during a run.
type Status int

const (
	// StatusPending: not looked at (yet), e.g. because the search limit of the run was reached.
	StatusPending Status = iota
	// StatusNew: found on Spotify and not in the playlist yet.
	StatusNew
	// StatusInPlaylist: found on Spotify and already in the playlist.
	StatusInPlaylist
	// StatusAdded: was added to the playlist in this run.
	StatusAdded
	// StatusNotFound: no confident match on Spotify. It is looked for again at the next run.
	StatusNotFound
	// StatusMismatch: a track with this name exists, but with another release date than on hardstyle.com.
	StatusMismatch
)

// Phase is the step a run is in.
type Phase int

const (
	PhaseIdle Phase = iota
	PhaseFetch
	PhaseSearch
	PhaseWrite
	PhaseDone
)

// EventKind tells which fields of an Event are set.
type EventKind int

const (
	// EvLog carries a line for the log in Msg.
	EvLog EventKind = iota
	// EvPhase carries the step (Phase) and its progress (Done of Total); Msg names what is processed.
	EvPhase
	// EvReleases carries the list of releases of this run.
	EvReleases
	// EvItem carries the new Status of the release at Index, with its URI, its date on hardstyle.com
	// (Date) and a note why it is missing (Note).
	EvItem
	// EvPlaylist carries the state of the playlist.
	EvPlaylist
	// EvRequests carries the number of Spotify requests of this run in Done.
	EvRequests
)

// Event reports the progress of a run to the front end.
type Event struct {
	Kind     EventKind
	Msg      string
	Phase    Phase
	Done     int
	Total    int
	Index    int
	Status   Status
	URI      string
	Date     string
	Note     string
	Releases []hardstyle.Track
	Playlist PlaylistStats
}

// PlaylistStats describes the playlist. Oldest and Newest are release dates on hardstyle.com.
type PlaylistStats struct {
	Count          int
	Oldest, Newest time.Time
}

// StatsFor describes a playlist from its tracks and the release dates the cache knows.
func StatsFor(uris []string, dates map[string]string) PlaylistStats {
	s := PlaylistStats{Count: len(uris)}
	for _, u := range uris {
		d, err := time.Parse("2006-01-02", dates[u])
		if err != nil {
			continue
		}
		if s.Oldest.IsZero() || d.Before(s.Oldest) {
			s.Oldest = d
		}
		if d.After(s.Newest) {
			s.Newest = d
		}
	}
	return s
}

// Emit receives the events of a run. It is called from several goroutines, a nil Emit drops everything.
type Emit func(Event)

func (e Emit) Send(ev Event) {
	if e != nil {
		e(ev)
	}
}

func (e Emit) Logf(format string, args ...any) {
	e.Send(Event{Kind: EvLog, Msg: fmt.Sprintf(format, args...)})
}
