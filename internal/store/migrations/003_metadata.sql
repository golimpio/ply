-- M6: metadata. Text columns default to '' and counts to 0 ("unknown"); ids that
-- must stay unique when absent (tmdb_id, tvdb_id) are NULL instead.

CREATE TABLE collections (
    id       INTEGER PRIMARY KEY,
    tmdb_id  INTEGER UNIQUE,
    name     TEXT NOT NULL DEFAULT '',
    overview TEXT NOT NULL DEFAULT '',
    poster   TEXT NOT NULL DEFAULT '',
    backdrop TEXT NOT NULL DEFAULT ''
);

CREATE TABLE titles (
    id             INTEGER PRIMARY KEY,
    kind           TEXT    NOT NULL CHECK (kind IN ('movie', 'series')),
    tmdb_id        INTEGER,
    imdb_id        TEXT    NOT NULL DEFAULT '',
    tvdb_id        INTEGER,
    title          TEXT    NOT NULL DEFAULT '',
    original_title TEXT    NOT NULL DEFAULT '',
    sort_title     TEXT    NOT NULL DEFAULT '',
    year           INTEGER NOT NULL DEFAULT 0,
    overview       TEXT    NOT NULL DEFAULT '',
    tagline        TEXT    NOT NULL DEFAULT '',
    release_date   TEXT    NOT NULL DEFAULT '', -- YYYY-MM-DD
    end_date       TEXT    NOT NULL DEFAULT '',
    status         TEXT    NOT NULL DEFAULT '',
    runtime_min    INTEGER NOT NULL DEFAULT 0,
    rating         REAL    NOT NULL DEFAULT 0,
    vote_count     INTEGER NOT NULL DEFAULT 0,
    certification  TEXT    NOT NULL DEFAULT '',
    poster         TEXT    NOT NULL DEFAULT '', -- image refs: "tmdb:/abc.jpg"
    backdrop       TEXT    NOT NULL DEFAULT '',
    logo           TEXT    NOT NULL DEFAULT '',
    collection_id  INTEGER REFERENCES collections (id) ON DELETE SET NULL,
    source         TEXT    NOT NULL DEFAULT 'tmdb' CHECK (source IN ('tmdb', 'nfo', 'mixed')),
    locked         INTEGER NOT NULL DEFAULT 0,  -- user edited: a refresh keeps the text fields
    next_air_date  TEXT,                        -- series: next_episode_to_air.air_date, YYYY-MM-DD
    fetched_at     INTEGER NOT NULL DEFAULT 0,  -- unix seconds; 0 = never fetched
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    UNIQUE (kind, tmdb_id)
);
CREATE INDEX titles_collection ON titles (collection_id);

-- Movies link straight to a title; episodes go through series_matches.
ALTER TABLE items ADD COLUMN title_id INTEGER REFERENCES titles (id) ON DELETE SET NULL;
ALTER TABLE items ADD COLUMN match_state TEXT NOT NULL DEFAULT 'pending'
    CHECK (match_state IN ('pending', 'matched', 'unmatched', 'manual', 'disabled'));
ALTER TABLE items ADD COLUMN needs_review INTEGER NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN match_score REAL NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN match_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN next_attempt_at INTEGER NOT NULL DEFAULT 0; -- unix seconds; 0 = due now
ALTER TABLE items ADD COLUMN local_poster TEXT NOT NULL DEFAULT '';      -- absolute paths of sidecar artwork
ALTER TABLE items ADD COLUMN local_backdrop TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN local_thumb TEXT NOT NULL DEFAULT '';
ALTER TABLE items ADD COLUMN fingerprint TEXT NOT NULL DEFAULT '';       -- move fallback, see the scanner
CREATE INDEX items_title ON items (title_id);
CREATE INDEX items_match ON items (match_state, next_attempt_at);
CREATE INDEX items_fingerprint ON items (size, fingerprint);

-- One row per show name (items.show). A show with episodes and no row here has
-- not been looked at yet and counts as pending.
CREATE TABLE series_matches (
    show            TEXT    NOT NULL PRIMARY KEY,
    title_id        INTEGER REFERENCES titles (id) ON DELETE SET NULL,
    match_state     TEXT    NOT NULL DEFAULT 'pending'
        CHECK (match_state IN ('pending', 'matched', 'unmatched', 'manual', 'disabled')),
    needs_review    INTEGER NOT NULL DEFAULT 0,
    match_score     REAL    NOT NULL DEFAULT 0,
    match_attempts  INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    local_poster    TEXT    NOT NULL DEFAULT '',
    local_backdrop  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX series_matches_title ON series_matches (title_id);
CREATE INDEX series_matches_match ON series_matches (match_state, next_attempt_at);

CREATE TABLE seasons (
    title_id INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    season   INTEGER NOT NULL,
    name     TEXT    NOT NULL DEFAULT '',
    overview TEXT    NOT NULL DEFAULT '',
    poster   TEXT    NOT NULL DEFAULT '',
    air_date TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (title_id, season)
);

CREATE TABLE episodes (
    title_id    INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    season      INTEGER NOT NULL,
    episode     INTEGER NOT NULL,
    name        TEXT    NOT NULL DEFAULT '',
    overview    TEXT    NOT NULL DEFAULT '',
    air_date    TEXT    NOT NULL DEFAULT '',
    runtime_min INTEGER NOT NULL DEFAULT 0,
    still       TEXT    NOT NULL DEFAULT '',
    rating      REAL    NOT NULL DEFAULT 0,
    PRIMARY KEY (title_id, season, episode)
);

CREATE TABLE people (
    id             INTEGER PRIMARY KEY,
    tmdb_id        INTEGER UNIQUE,  -- NULL for people that only an NFO names (deduped by exact name)
    name           TEXT    NOT NULL,
    profile        TEXT    NOT NULL DEFAULT '',
    biography      TEXT    NOT NULL DEFAULT '',
    birthday       TEXT    NOT NULL DEFAULT '',
    deathday       TEXT    NOT NULL DEFAULT '',
    place_of_birth TEXT    NOT NULL DEFAULT '',
    known_for      TEXT    NOT NULL DEFAULT '',
    imdb_id        TEXT    NOT NULL DEFAULT '',
    fetched_at     INTEGER NOT NULL DEFAULT 0 -- 0 = only the credits' name and photo so far
);
CREATE INDEX people_name ON people (name);

CREATE TABLE credits (
    title_id  INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    person_id INTEGER NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    kind      TEXT    NOT NULL CHECK (kind IN ('cast', 'crew')),
    job       TEXT    NOT NULL DEFAULT '', -- '' for cast
    character TEXT    NOT NULL DEFAULT '',
    ord       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (title_id, person_id, kind, job)
);
CREATE INDEX credits_person ON credits (person_id);

CREATE TABLE genres (
    id   INTEGER PRIMARY KEY, -- TMDB genre id; negative when only an NFO names it
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE title_genres (
    title_id INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    genre_id INTEGER NOT NULL REFERENCES genres (id) ON DELETE CASCADE,
    PRIMARY KEY (title_id, genre_id)
);
CREATE INDEX title_genres_genre ON title_genres (genre_id);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
