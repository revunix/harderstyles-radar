package spotify

import (
	"hardstylesync/internal/cache"
	"slices"
)

// Stored turns a track into the form the cache keeps for the evaluation. TrackFromStored turns it back; no field
// of the track is lost on the way.
func (t Track) Stored() cache.StoredTrack {
	return cache.StoredTrack{
		URI: t.URI, Name: t.Name, Artists: slices.Clone(t.Artists), ArtistIDs: slices.Clone(t.ArtistIDs),
		ReleaseDate: t.ReleaseDate, DatePrecision: t.DatePrecision, Duration: t.Duration,
		AlbumID: t.Album.ID, AlbumName: t.Album.Name, AlbumType: t.Album.Type, AlbumTracks: t.Album.Tracks,
	}
}

// TrackFromStored is the track the cache kept with Stored.
func TrackFromStored(s cache.StoredTrack) Track {
	return Track{
		URI: s.URI, Name: s.Name, Artists: slices.Clone(s.Artists), ArtistIDs: slices.Clone(s.ArtistIDs),
		ReleaseDate: s.ReleaseDate, DatePrecision: s.DatePrecision, Duration: s.Duration,
		Album: Album{ID: s.AlbumID, Name: s.AlbumName, Type: s.AlbumType, Tracks: s.AlbumTracks},
	}
}
