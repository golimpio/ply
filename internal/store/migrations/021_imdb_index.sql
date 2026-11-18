-- The filmography's in_library check looks a title up by each of its three ids
-- (tmdb, imdb, tvdb) in turn. tmdb and tvdb are covered by UNIQUE (kind, id);
-- imdb had no index, so its lookup scanned every title of the kind, per
-- filmography entry. This is that index. Not UNIQUE (imdb_id defaults to '',
-- which repeats) and not partial (a partial index on imdb_id <> '' cannot serve
-- a lookup whose condition is an equality with the entry's id).
CREATE INDEX titles_imdb ON titles (kind, imdb_id);

-- Three indexes no query reads: the filmography is only ever read by its
-- primary key (person_id, ord) -- the API sorts by popularity in Go -- and
-- episodes are read by title_id, the ratings join going from episodes to
-- episode_ratings' primary key, never the reverse. Each cost a B-tree write
-- on every filmography replace and episode id mapping.
DROP INDEX person_filmography_person_pop;
DROP INDEX person_filmography_person_year;
DROP INDEX episodes_imdb;

-- The planner chooses between indexes by statistics, and this database has
-- never had any (nothing runs ANALYZE). With them, the byte-range prefix
-- lookups (ItemsBelow, the trailer folder probes) seek the path index instead
-- of scanning by kind. One pass at upgrade; the shapes it decides between stay
-- the right shapes as the library grows.
ANALYZE;
