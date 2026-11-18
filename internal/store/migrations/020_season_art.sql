-- Local season posters (docs/M6-METADATA.md): a sidecar next to the show
-- (seasonNN-poster.jpg) or in the season's own folder (Season NN/poster.jpg),
-- served over the provider's poster. Absolute path, '' = none.
ALTER TABLE seasons ADD COLUMN local_poster TEXT NOT NULL DEFAULT '';