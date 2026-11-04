package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"ply/internal/library"
	"ply/internal/store"
)

// rereadPrefix starts the settings that remember a folder whose items are to be read
// again as the type it was given says (Scanner.Reread). A folder given a type on the
// command line for the first time is known to be one only until it is saved, so the
// note is made before it is saved and forgotten once the items are read: a start that
// ends in between finds the note at the next one.
const rereadPrefix = "m7.reread."

// prepareFolders is what the start does with the library folders before the scanner
// gets them: it takes the types the saved folders have now (before), notes the folders
// a flag types for the first time as to be read again, and only then saves the flag
// folders (startupRoots). The order is the point: once a folder is saved it is no longer
// new, so the note has to exist by then.
func prepareFolders(ctx context.Context, st *store.Store, flags []library.Root, log *slog.Logger) (roots []library.Root, before map[string]string, err error) {
	if before, err = storedKinds(ctx, st); err != nil {
		return nil, nil, err
	}
	if err = markRereads(ctx, st, firstTyped(flags, before)); err != nil {
		return nil, nil, err
	}
	roots, err = startupRoots(ctx, st, flags, log)
	return roots, before, err
}

// markRereads notes the folders to read again. Call it before the folders are saved.
func markRereads(ctx context.Context, st *store.Store, paths []string) error {
	for _, p := range paths {
		if err := st.SetSetting(ctx, rereadPrefix+p, "1"); err != nil {
			return fmt.Errorf("noting that %s is to be read again: %w", p, err)
		}
	}
	return nil
}

// pendingRereads are the folders that were noted and not yet read again, sorted.
func pendingRereads(ctx context.Context, st *store.Store) ([]string, error) {
	noted, err := st.SettingsWithPrefix(ctx, rereadPrefix)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(noted))
	for k := range noted {
		paths = append(paths, strings.TrimPrefix(k, rereadPrefix))
	}
	slices.Sort(paths)
	return paths, nil
}

// rereadPending has the scanner read the noted folders again, in the background, and
// forgets each one once it has been looked at. One whose reading failed stays noted for
// the next start. Call it after the scanner has its roots.
func rereadPending(ctx context.Context, st *store.Store, scanner *library.Scanner, log *slog.Logger) {
	paths, err := pendingRereads(ctx, st)
	if err != nil {
		log.Warn("reading the folders to read again", "err", err)
		return
	}
	if len(paths) == 0 {
		return
	}
	scanner.Reread(paths, forgetReread(ctx, st, log))
}

// forgetReread is what is done with a folder the scanner has looked at: the note goes
// when the folder was read, and stays when it could not be, for the next start.
func forgetReread(ctx context.Context, st *store.Store, log *slog.Logger) func(root string, err error) {
	return func(root string, err error) {
		if err != nil {
			return
		}
		if err := st.DeleteSetting(ctx, rereadPrefix+root); err != nil && ctx.Err() == nil {
			log.Warn("forgetting a folder that was read again", "folder", root, "err", err)
		}
	}
}
