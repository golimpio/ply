-- Extended person filmography (docs/M6-METADATA.md): an artist's broader work outside the
-- library (from TMDB combined_credits, fallback to TVDB extended), lazy-loaded when their page is opened.
-- ord keeps the provider's popularity/relevance order (0 is most notable).

CREATE TABLE person_filmography (
    person_id      INTEGER NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    ord            INTEGER NOT NULL,
    kind           TEXT    NOT NULL CHECK (kind IN ('movie', 'series')),
    tmdb_id        INTEGER NOT NULL DEFAULT 0,
    tvdb_id        INTEGER NOT NULL DEFAULT 0,
    imdb_id        TEXT    NOT NULL DEFAULT '',
    title          TEXT    NOT NULL DEFAULT '',
    original_title TEXT    NOT NULL DEFAULT '',
    year           INTEGER NOT NULL DEFAULT 0,
    release_date   TEXT    NOT NULL DEFAULT '',
    character      TEXT    NOT NULL DEFAULT '',
    job            TEXT    NOT NULL DEFAULT '',
    department     TEXT    NOT NULL DEFAULT 'Acting',
    credit_kind    TEXT    NOT NULL DEFAULT 'cast' CHECK (credit_kind IN ('cast', 'crew')),
    poster         TEXT    NOT NULL DEFAULT '',
    backdrop       TEXT    NOT NULL DEFAULT '',
    rating         REAL    NOT NULL DEFAULT 0,
    vote_count     INTEGER NOT NULL DEFAULT 0,
    popularity     REAL    NOT NULL DEFAULT 0,
    episode_count  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (person_id, ord)
);

CREATE INDEX person_filmography_person_pop ON person_filmography (person_id, department, popularity DESC);
CREATE INDEX person_filmography_person_year ON person_filmography (person_id, year DESC);

-- wikidata_id links the person to Wikidata/Wikipedia.
-- filmography_ref is the person and language they were fetched for ('tmdb:6384@en-US', '' = never).
-- filmography_checked is when (unix seconds, 0 = never); a person with no external credits is checked too.
ALTER TABLE people ADD COLUMN wikidata_id TEXT NOT NULL DEFAULT '';
ALTER TABLE people ADD COLUMN filmography_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE people ADD COLUMN filmography_checked INTEGER NOT NULL DEFAULT 0;
