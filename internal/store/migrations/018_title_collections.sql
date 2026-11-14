-- Migration 018: title_collections junction table and query optimization
--
-- A film or series can belong to more than one collection (e.g. franchise collections,
-- studio collections). Replaces the 1:N titles.collection_id limitation with an M:N
-- junction table, while preserving backwards compatibility.

CREATE TABLE title_collections (
    title_id      INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    collection_id INTEGER NOT NULL REFERENCES collections (id) ON DELETE CASCADE,
    ord           INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (title_id, collection_id)
);

CREATE INDEX title_collections_collection ON title_collections (collection_id);

-- Backfill from existing titles.collection_id
INSERT OR IGNORE INTO title_collections (title_id, collection_id, ord)
SELECT id, collection_id, 0 FROM titles WHERE collection_id IS NOT NULL;

-- Optimize job queue polling: sort directly by id rather than temporary B-tree sort on state
DROP INDEX IF EXISTS jobs_unfinished;
CREATE INDEX jobs_unfinished ON jobs (id) WHERE state IN ('queued', 'running');
