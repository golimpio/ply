-- M11: a watched file that goes leaves a mark for the file that replaces it (docs/DESIGN.md,
-- "Watch state"). An upgrade (a Radarr or Sonarr import) puts a new file at another path and
-- deletes the old one; the state of the old file is on its progress row, which goes with it.
--
-- A mark is by the title the file was linked to: a film (season 0, episode 0), or an episode of a
-- series (the title of its show, its season and its number). When a file is linked to the title
-- (a film), or one of that number is in its show (an episode), it is given the state of the mark
-- and the mark goes. removed_at (unix seconds) is when the file went: marks older than 90 days are
-- dropped, and while they last they keep their title from being pruned.
CREATE TABLE title_watch (
    title_id   INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    season     INTEGER NOT NULL DEFAULT 0,
    episode    INTEGER NOT NULL DEFAULT 0,
    watched_at INTEGER NOT NULL CHECK (watched_at > 0),
    play_count INTEGER NOT NULL CHECK (play_count > 0),
    removed_at INTEGER NOT NULL,
    PRIMARY KEY (title_id, season, episode)
);
