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
  - With no season in the name and no `Season N` folder, the season is 1.
  - When the first folder below the root is a `Season N`/`Specials` folder, or the file is straight in the root, the root is
    the show's own folder: the show is the file name's own show if it has one, else the root folder's name.
- There is no `mixed` type. `ParseName`'s guessing survives only as the **one-time guess** for a
  `-media` folder whose type isn't known yet: more than half its files named `SxxEyy` or `1x02` means `shows`, else `movies`.
  The guess is stored; Settings shows it as "guessed", and the user can change it.
  - It is made by a scan that saw the whole folder without trouble (every file probed, none still settling).
  - A folder that already had items (indexed before types existed) is **not restructured** by the guess: its existing
    items stay as they are, with their shows, matches and progress, and only the files new to the scan are read as the
    guessed type. "Existing" comes from the database, not from the process: the first `Open` of a database by this
    version stamps the time in the `m7.since` setting (`Store.TypedSince`), and an item whose `added_at` is before it was
    left by an earlier version. (Ids are no use, SQLite reuses them; and a restart before the guess must not turn the first
    run's files into old ones.)
    The log says how many existing items the guess leaves ("2 existing episodes kept as they are"). They are read again
    only when the user changes the type in Settings. A file of such a folder that is re-encoded keeps the reading it had:
    a re-index writes the file's own columns (size, mtime, streams), not what its path says.
  - A type taken from the stored `folders` row reads nothing again. A stored type that a flag **changes** at start is
    read again, like a PATCH. A type given on the command line for a folder that was **not saved before** (`-shows DIR`
    for a folder that was `-media` in an older version) is read again too (`Scanner.Reread`), because the files already
    there would otherwise be read one way and the new ones another (`/tv/Lost (2004)/Season 1/Lost.S01E01.mkv` is the show
    "Lost" to the old reader and "Lost (2004)" to a shows folder); the log warns with the number of items that change.
    That a folder is to be read again is noted in the settings (`m7.reread.<path>`) **before** the flag folders are saved
    (once saved a folder is no longer new) and forgotten once it has been read, so a start that ends in between
    (`prepareFolders`, `rereadPending`) does not lose it: the next start reads it.
  - A show that is **renamed** by a re-reading (all of its episodes are read as the episodes of one other show, which had
    none and gets no others: "Lost" becomes "Lost (2004)") keeps its manual match: the series row moves to the new name.
    Anything else (a type change that makes episodes movies, several shows read as one, a new name with a match of its
    own) archives the match for the old show, as below.

## Command line
- `-movies DIR` and `-shows DIR` (repeatable) give the folder and its type. In the UI the type is locked.
- `-media DIR` keeps working. Its type is taken from the `folders` table when the folder is already
  known, otherwise guessed on the first scan (see above). It stays editable in the UI.
- Command-line folders are listed with `source: "flag"`. They can't be removed in the UI, because they would
  come back at the next start. The UI says "set at startup".
- ply may start with no folders at all. The log then says to add one in Settings.

## Store (`005_folders.sql`, `006_archive_slots.sql`)
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
    tmdb_id      INTEGER NOT NULL DEFAULT 0,  -- a movie's own manual match (tmdb_kind 'movie')
    tmdb_kind    TEXT    NOT NULL DEFAULT '',
    position_sec REAL    NOT NULL DEFAULT 0,
    duration_sec REAL    NOT NULL DEFAULT 0,
    watched      INTEGER NOT NULL DEFAULT 0,
    progress_at  INTEGER NOT NULL DEFAULT 0,
    archived_at  INTEGER NOT NULL,
    -- 006: the match of the show an episode was in, apart from the movie's
    series_id    INTEGER NOT NULL DEFAULT 0,
    series_show  TEXT    NOT NULL DEFAULT ''  -- the show it was for ('' = any: the row predates the column)
);
```
- A path can hold a movie's match and a show's at once (a file that was a movie, then an episode, then a movie again), each
  in its slot; each slot holds the latest. A show's match comes back only for an episode of the **same show**.
- **Command-line folders** are upserted into `folders` at every start: `-movies` and `-shows` set the kind, and `-media` keeps the stored one.
  Whether a folder came from the command line is per run, not stored.
- **Active roots** = the command-line folders plus every UI folder (`folders` rows). Removing a UI folder deletes its row.
- **Archive on remove:** each item of the removed folder that has progress or a manual match is archived
  (progress, and a manual match's TMDB id and kind: a movie's own, or its show's), then all its items are deleted;
  streams, keyframes and so on cascade.
  - `DELETE ?forget=1` skips the archive, and **deletes the archive rows of the folder's files** too, a match still
    waiting to be applied included.
  - **Order of a removal**, under the folders lock: (1) `SetRoots` with the roots that are left, so that no scan or
    watcher event brings the files back (a root that is no longer configured has its files removed without an archive);
    (2) `DropRoot`, which waits for a running scan and archives; (3) the row is deleted. If `DropRoot` fails, `SetRoots` is
    called again with the folder, and the folder stays. `DropRoot` waits `dropWait` (30 s) for the running pass, then
    returns `ErrScanBusy` (503 with `Retry-After`). A pass that left the folder's files unseen because it was taken out
    (`pass.skipped`) removes **nothing** of the folder and notes no scan of it, even if the folder is back by the time it
    ends, and the same for a partial pass: a folder that is put back keeps every row, progress and match.
- **Restore on add:** when the scanner indexes a file whose path is in `item_archive` it restores the progress (the row loses it).
  A manual match is applied through the worker by TMDB id, **and stays in the archive until it is applied**
  (`ForgetArchivedMatch`, called by the worker side): the scanner reports every archived match whose item is in the library,
  after each scan, so a match is not lost when TMDB is off or out of reach. While online lookups are off nothing is
  reported (`SetMatchGate`); `ReportMatchesIfDue` (every 10 s from `main`) reports them once they are on.
  - A size mismatch restores only the match.
  - A file that moves onto an archived path drops that archive row: the file there was another one.
- **Changing a folder's kind** (the PATCH, or a flag that changes the stored kind) re-parses its items (kind, title, show,
  season, episode), those whose reading differs. It clears every match that isn't manual, and drops manual matches whose TMDB
  kind no longer fits (movie vs tv). **A manual match that is dropped is kept in `item_archive` by path**, and comes back when
  the kind does (it is reported like any archived match). The worker then matches again. Progress is never touched.
  - The same for an episode that leaves its show (it becomes a movie, or its show's name changes): the show's manual match
    is archived for it, with the show's name, and comes back only for an episode of that show.
  - A file **moved** into a folder of another type (a movie into a shows folder) is read again the same way (`MoveItem`);
    a plain rename keeps its match.
- **Review:** an episode with no number is flagged `needs_review` unless it is in an `Extras`, `Featurettes` or
  `Behind The Scenes` folder. A show's entry in `GET /api/metadata/review` lists its flagged episodes (`episodes: [{id, path}]`).

## Overlap
- Adding a folder that is inside, or contains, an active folder is refused (400, naming the other folder).
- Paths are compared as directories, not as spelled: through symlinks (`os.SameFile` while walking the parents), and on a disk
  that ignores case (APFS by default) another case is the same folder. A folder that is already there under another
  spelling is a 409. A folder that is not on disk any more is compared by name.
- Command-line folders keep today's nesting behaviour.

## API (all JSON; errors `{"error": "..."}`)
- `GET /api/folders` returns `[{id, path, kind, guessed, source: "flag"|"ui", kindLocked, items, lastScanAt, state: "ok"|"missing"|"error", error}]`.
- `POST /api/folders {path, kind}` → 201 with the folder.
  - 400 for: a path that isn't absolute, isn't a directory, can't be read, overlaps another folder, or a bad kind.
  - 409 when the folder is already there. A scan of the folder starts at once.
- `PATCH /api/folders/{id} {kind}` → 200. 409 if `kindLocked`.
- `DELETE /api/folders/{id}[?forget=1]` → 204. 409 for a command-line folder.
- `GET /api/fs[?path=DIR]` lists **directories only**: `{path, parent, dirs: [{name, path}]}`.
  - Dot-folders are left out, and so are files. A path with a dot component (`/home/me/.config`) is refused with 403.
  - With no path it returns `places`: home, `/Volumes/*` (darwin), `/media/*` and `/mnt/*` (linux), and the
    current folders. Entries that are links to `/` (`/Volumes/Macintosh HD`) are left out.
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
- Pruning `item_archive`: rows of folders nobody adds back stay for ever. They are small (one per file with progress or a manual match).
- TVDB for TV shows (the user asked). It needs a TVDB project key plus, under TVDB's user-supported
  model, each user's subscriber PIN. Planned as M8, once the user decides on the key.
