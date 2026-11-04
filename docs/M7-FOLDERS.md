# M7 — library folders managed in the web UI

The user's request: set one or more media folders in Settings, each with a type, **TV shows or
Movies, never mixed**. Add and remove them from the web page, with no restart.

## Folder types
- `movies`: every file is a movie, never an episode.
  - The title and year come from the file name, or from the parent folder when the file name is junk (`movie.mkv`, `video_ts`).
- `shows`: every file is an episode.
  - The show is the first folder below the root: `<root>/<Show>/Season 2/x.mkv` gives `<Show>`.
  - The season comes from `SxxEyy` or `1x02` in the name, else from a `Season N` or `S2` parent folder. `Specials` means season 0.
  - The episode number comes from, in order: `SxxEyy`, `1x02`, `E02`, `Episode 2`, or a leading number (`02 - Title`).
  - A file with no episode number is still an episode (episode 0), flagged `needs_review`.
- There is no `mixed` type. `ParseName`'s guessing survives only as the **one-time guess** for a
  `-media` folder whose type isn't known yet: more than half its files named `SxxEyy` means `shows`, else `movies`.
  The guess is stored; Settings shows it as "guessed", and the user can change it.

## Command line
- `-movies DIR` and `-shows DIR` (repeatable) give the folder and its type. In the UI the type is locked.
- `-media DIR` keeps working. Its type is taken from the `folders` table when the folder is already
  known, otherwise guessed on the first scan (see above). It stays editable in the UI.
- Command-line folders are listed with `source: "flag"`. They can't be removed in the UI, because they would
  come back at the next start. The UI says "set at startup".
- ply may start with no folders at all. The log then says to add one in Settings.

## Store (`005_folders.sql`)
```sql
CREATE TABLE folders (
    id         INTEGER PRIMARY KEY,
    path       TEXT    NOT NULL UNIQUE,   -- absolute, cleaned
    kind       TEXT    NOT NULL CHECK (kind IN ('movies', 'shows')),
    guessed    INTEGER NOT NULL DEFAULT 0, -- the kind was guessed, not chosen
    added_at   INTEGER NOT NULL
);
-- What a removed folder's files leave behind, so adding the folder back restores it.
CREATE TABLE item_archive (
    path         TEXT    PRIMARY KEY,
    size         INTEGER NOT NULL,
    tmdb_id      INTEGER NOT NULL DEFAULT 0,  -- only for a manual (user-fixed) match
    tmdb_kind    TEXT    NOT NULL DEFAULT '',
    position_sec REAL    NOT NULL DEFAULT 0,
    duration_sec REAL    NOT NULL DEFAULT 0,
    watched      INTEGER NOT NULL DEFAULT 0,
    progress_at  INTEGER NOT NULL DEFAULT 0,
    archived_at  INTEGER NOT NULL
);
```
- **Command-line folders** are upserted into `folders` at every start: `-movies` and `-shows` set the kind, and `-media` keeps the stored one.
  Whether a folder came from the command line is per run, not stored.
- **Active roots** = the command-line folders plus every UI folder (`folders` rows). Removing a UI folder deletes its row.
- **Archive on remove:** each item of the removed folder is archived (progress, and a manual match's TMDB id),
  then deleted; streams, keyframes and so on cascade.
  - `DELETE ?forget=1` skips the archive.
- **Restore on add:** when the scanner inserts a file whose path is in `item_archive`, it restores the progress,
  and a manual match is applied through the worker by TMDB id. The archive row is then deleted.
  - A size mismatch restores only the match.
- **Changing a folder's kind** re-parses its items (kind, title, show, season, episode). It clears every match that isn't manual,
  and drops manual matches whose TMDB kind no longer fits (movie vs tv). The worker then matches again.

## Overlap
- Adding a folder that is inside, or contains, an active folder is refused (400, naming the other folder).
- Command-line folders keep today's nesting behaviour.

## API (all JSON; errors `{"error": "..."}`)
- `GET /api/folders` returns `[{id, path, kind, guessed, source: "flag"|"ui", kindLocked, items, lastScanAt, state: "ok"|"missing"|"error", error}]`.
- `POST /api/folders {path, kind}` → 201 with the folder.
  - 400 for: a path that isn't absolute, isn't a directory, can't be read, overlaps another folder, or a bad kind.
  - 409 when the folder is already there. A scan of the folder starts at once.
- `PATCH /api/folders/{id} {kind}` → 200. 409 if `kindLocked`.
- `DELETE /api/folders/{id}[?forget=1]` → 204. 409 for a command-line folder.
- `GET /api/fs[?path=DIR]` lists **directories only**: `{path, parent, dirs: [{name, path}]}`.
  - Dot-folders are left out, and so are files.
  - With no path it returns `places`: home, `/Volumes/*` (darwin), `/media/*` and `/mnt/*` (linux), and the
    current folders.
  - An unreadable directory gives 403 with the OS error. A missing one gives 404.
- **Security:** `/api/fs` and every folder mutation require the token when one is set. They never send
  `Access-Control-Allow-Origin`, and they refuse a cross-site request: an `Origin` that isn't this server, or
  `Sec-Fetch-Site: cross-site`, gets 403. Otherwise any page on the LAN could list the server's folders.

## Ownership
- **velvet-viper:**
  - migration, store (`folders`, archive and restore helpers);
  - config (`-movies`, `-shows`);
  - `library.ParseNameIn(root Root, path)` in nameparse.go (the type rules above), the type guess, and `library.Root{Path, Kind}`;
  - the API handlers and the `main` wiring.
- **rare-tiger:**
  - `Scanner.SetRoots([]library.Root)`: live, triggers a scan of new roots and calls `ParseNameIn`;
  - the archive call when a root is dropped, and the restore call on insert;
  - `Watcher.SetRoots([]string)`;
  - the Settings "Library folders" UI with the folder browser.
- Each side's code is reviewed by the other, and every fix has a test that fails without it.

## Later (not M7)
- TVDB for TV shows (the user asked). It needs a TVDB project key plus, under TVDB's user-supported
  model, each user's subscriber PIN. Planned as M8, once the user decides on the key.
