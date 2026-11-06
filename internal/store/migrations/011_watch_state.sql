-- M11: watch state (docs/DESIGN.md, "Watch state").
--
-- progress kept a flag, watched, and a time that said when the row last changed. It now keeps
-- what the flag stood for as a fact: watched_at, when the file was last watched to the end or
-- marked watched (unix seconds, 0 = not watched), and play_count, how many times. The flag
-- is gone: watched is watched_at <> 0, and the table checks that the two columns agree.
--
-- A file with no row is not started and not watched. Marking unwatched deletes the row.

CREATE TABLE progress_new (
    item_id      INTEGER PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    position_sec REAL    NOT NULL DEFAULT 0,
    duration_sec REAL    NOT NULL DEFAULT 0,
    watched_at   INTEGER NOT NULL DEFAULT 0,  -- unix seconds; 0 = not watched
    play_count   INTEGER NOT NULL DEFAULT 0,  -- 0 exactly when watched_at is 0
    updated_at   INTEGER NOT NULL,            -- unix nanoseconds, so that back-to-back writes still order
    CHECK ((watched_at = 0) = (play_count = 0))
);

-- A file that was watched counts as watched once, at the time its row last changed (updated_at is
-- nanoseconds; a row from before the clock was kept in seconds still gets a time other than 0).
INSERT INTO progress_new (item_id, position_sec, duration_sec, watched_at, play_count, updated_at)
SELECT item_id, position_sec, duration_sec,
       CASE WHEN watched <> 0 THEN MAX(updated_at / 1000000000, 1) ELSE 0 END,
       CASE WHEN watched <> 0 THEN 1 ELSE 0 END,
       updated_at
FROM progress;

DROP TABLE progress;
ALTER TABLE progress_new RENAME TO progress;

-- What a removed folder keeps of a file (M7) keeps the same two facts.
ALTER TABLE item_archive ADD COLUMN watched_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE item_archive ADD COLUMN play_count INTEGER NOT NULL DEFAULT 0;
UPDATE item_archive SET watched_at = MAX(progress_at / 1000000000, 1), play_count = 1 WHERE watched <> 0;
ALTER TABLE item_archive DROP COLUMN watched;
