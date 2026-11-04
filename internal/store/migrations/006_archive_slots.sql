-- M7: an archive row keeps two manual matches apart, a movie's own (tmdb_id, tmdb_kind
-- 'movie') and the match of the show an episode was in (series_id, with series_show
-- naming that show: the match comes back only for an episode of the same show).
-- Before, one slot held either, and a show's match went to whatever show the file
-- was called after.
ALTER TABLE item_archive ADD COLUMN series_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE item_archive ADD COLUMN series_show TEXT NOT NULL DEFAULT ''; -- '' = any show (the row predates the column)
UPDATE item_archive SET series_id = tmdb_id, tmdb_id = 0, tmdb_kind = '' WHERE tmdb_kind = 'series';
