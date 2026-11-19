package plex

import (
	"context"
	"errors"
)

// ErrNoArt means the item has no selected image of the requested slot.
var ErrNoArt = errors.New("plex: no such artwork")

// Source reads a Plex library. The API source (a running server + token) and the
// DB source (a read-only copy of the database + bundles) both implement it, so
// the migration logic does not care which was used.
type Source interface {
	// Libraries returns the library sections and the folders they scan.
	Libraries(ctx context.Context) ([]Library, error)
	// Items returns the movies, shows, seasons and episodes, flattened. A season
	// or episode carries Show/Season/Episode so the migration can group them.
	Items(ctx context.Context) ([]Item, error)
	// Art returns the selected image bytes for slot "poster" or "art" (backdrop).
	// The API source returns the exact selection; the DB source resolves the
	// bundle (uploaded poster preferred), which is approximate when several
	// downloaded candidates exist — prefer the API source for artwork fidelity.
	Art(ctx context.Context, it Item, slot string) ([]byte, error)
	Close() error
}
