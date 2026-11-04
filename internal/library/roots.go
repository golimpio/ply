package library

// MergeRoots is the list of active roots: the command-line folders in the order
// given, then the saved ones (the folders table) that are not among them. A
// command-line folder with no type (-media) takes the saved type of the same
// path, if there is one; a typed one (-movies, -shows) keeps its own.
func MergeRoots(flags, saved []Root) []Root {
	kinds := make(map[string]string, len(saved))
	for _, r := range saved {
		kinds[r.Path] = r.Kind
	}
	seen := make(map[string]bool, len(flags)+len(saved))
	out := make([]Root, 0, len(flags)+len(saved))
	for _, r := range flags {
		if seen[r.Path] {
			continue
		}
		seen[r.Path] = true
		if r.Kind == "" {
			r.Kind = kinds[r.Path]
		}
		out = append(out, r)
	}
	for _, r := range saved {
		if !seen[r.Path] {
			seen[r.Path] = true
			out = append(out, r)
		}
	}
	return out
}
