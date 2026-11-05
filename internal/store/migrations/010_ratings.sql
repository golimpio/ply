-- M10: ratings (docs/M10-RATINGS.md).
--
-- title_ratings holds what is fetched from outside: IMDb's dataset (imdb) and MDBList's answer
-- (rt_critics, rt_audience, metacritic, letterboxd, trakt). TMDB's score is not here: it is
-- titles.rating and titles.vote_count, and the API reads it from there.
--
-- value is on the scale of the source (IMDb 10, Metacritic 100, Letterboxd 5, ...); the API
-- turns it into score100. state is for Rotten Tomatoes: fresh, rotten or certified on
-- rt_critics, upright, spilled or verified_hot on rt_audience; '' for the others.
CREATE TABLE title_ratings (
    title_id   INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    source     TEXT    NOT NULL CHECK (source IN ('imdb', 'tmdb', 'rt_critics', 'rt_audience', 'metacritic', 'letterboxd', 'trakt')),
    value      REAL    NOT NULL,
    scale      REAL    NOT NULL CHECK (scale > 0),
    votes      INTEGER NOT NULL DEFAULT 0,
    state      TEXT    NOT NULL DEFAULT '',
    url        TEXT    NOT NULL DEFAULT '',
    fetched_at INTEGER NOT NULL,                 -- unix seconds
    PRIMARY KEY (title_id, source)
);

-- Episodes have IMDb ratings only. They are keyed by the IMDb id (tconst), not by the episode
-- row: a refresh deletes and writes a season's episodes again, and the rating would go with them.
CREATE TABLE episode_ratings (
    imdb_id    TEXT    NOT NULL,
    source     TEXT    NOT NULL CHECK (source = 'imdb'),
    value      REAL    NOT NULL,
    scale      REAL    NOT NULL CHECK (scale > 0),
    votes      INTEGER NOT NULL DEFAULT 0,
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (imdb_id, source)
);

-- An episode's IMDb id (tt...), '' = not known. imdb_src says where it came from: 'dataset' (mapped
-- from IMDb's title.episode.tsv.gz by season and episode number, which a later mapping may correct or
-- clear), 'provider' (TMDB's or TVDB's own data, which the mapping never overwrites), '' (no id).
ALTER TABLE episodes ADD COLUMN imdb_id TEXT NOT NULL DEFAULT '';
ALTER TABLE episodes ADD COLUMN imdb_src TEXT NOT NULL DEFAULT '' CHECK (imdb_src IN ('', 'provider', 'dataset'));
CREATE INDEX episodes_imdb ON episodes (imdb_id) WHERE imdb_id <> '';

-- When MDBList was last asked about the title (unix seconds, 0 = never). A title it has nothing for
-- is also checked: it is asked again in 14 days, not at every look.
ALTER TABLE titles ADD COLUMN ratings_checked INTEGER NOT NULL DEFAULT 0;
