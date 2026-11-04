package library

// Folder types: every file under a movies root is a movie, every file under a
// shows root an episode. There is no mixed type (docs/M7-FOLDERS.md).
const (
	KindMovies = "movies"
	KindShows  = "shows"
)

// Root is a media folder and its type.
type Root struct {
	Path string // absolute, cleaned
	Kind string // KindMovies or KindShows
}

// ValidKind reports whether k is a folder type.
func ValidKind(k string) bool { return k == KindMovies || k == KindShows }
