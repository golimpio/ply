-- Trailers (docs/M6-METADATA.md, "Trailers"): a title's trailers and teasers at YouTube or Vimeo,
-- best first (ord 0 is the one to play), fetched the first time the title's page asks for them.
-- key is the video's id at the site, checked when it is stored: it is all a client builds a player from.
CREATE TABLE title_videos (
    title_id     INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    ord          INTEGER NOT NULL,
    site         TEXT    NOT NULL CHECK (site IN ('youtube', 'vimeo')),
    key          TEXT    NOT NULL CHECK (key <> ''),
    name         TEXT    NOT NULL DEFAULT '',
    type         TEXT    NOT NULL DEFAULT '' CHECK (type IN ('', 'trailer', 'teaser')),
    language     TEXT    NOT NULL DEFAULT '',
    official     INTEGER NOT NULL DEFAULT 0 CHECK (official IN (0, 1)),
    published_at TEXT    NOT NULL DEFAULT '', -- YYYY-MM-DD, '' unknown
    PRIMARY KEY (title_id, ord)
);

-- videos_ref is the title the videos were fetched for, at its provider, and the language they were
-- asked in ('tmdb:603@en-US', 'tvdb:81189@en-US'; '' = never): when either changes they are fetched again. videos_checked is when
-- (unix seconds, 0 = never); a title with no trailers is checked too.
ALTER TABLE titles ADD COLUMN videos_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE titles ADD COLUMN videos_checked INTEGER NOT NULL DEFAULT 0;
