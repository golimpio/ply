CREATE TABLE items (
    id           INTEGER PRIMARY KEY,
    kind         TEXT    NOT NULL CHECK (kind IN ('movie', 'episode')),
    title        TEXT    NOT NULL DEFAULT '',
    year         INTEGER NOT NULL DEFAULT 0,
    show         TEXT    NOT NULL DEFAULT '',
    season       INTEGER NOT NULL DEFAULT 0,
    episode      INTEGER NOT NULL DEFAULT 0,
    path         TEXT    NOT NULL UNIQUE, -- raw bytes as the OS reports them; never normalised
    root         TEXT    NOT NULL,
    size         INTEGER NOT NULL,
    mtime        INTEGER NOT NULL,        -- unix nanoseconds
    container    TEXT    NOT NULL DEFAULT '',
    duration_sec REAL    NOT NULL DEFAULT 0,
    bitrate      INTEGER NOT NULL DEFAULT 0,
    added_at     INTEGER NOT NULL,        -- unix seconds
    updated_at   INTEGER NOT NULL
);
CREATE INDEX items_root ON items (root);
CREATE INDEX items_browse ON items (kind, show, season, episode);

CREATE TABLE streams (
    item_id         INTEGER NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    idx             INTEGER NOT NULL,     -- ffprobe stream index
    type            TEXT    NOT NULL,     -- video | audio | subtitle
    codec           TEXT    NOT NULL DEFAULT '',
    codec_tag       TEXT    NOT NULL DEFAULT '',
    profile         TEXT    NOT NULL DEFAULT '',
    width           INTEGER NOT NULL DEFAULT 0,
    height          INTEGER NOT NULL DEFAULT 0,
    pix_fmt         TEXT    NOT NULL DEFAULT '',
    color_transfer  TEXT    NOT NULL DEFAULT '',
    channels        INTEGER NOT NULL DEFAULT 0,
    language        TEXT    NOT NULL DEFAULT '',
    title           TEXT    NOT NULL DEFAULT '',
    is_default      INTEGER NOT NULL DEFAULT 0,
    forced          INTEGER NOT NULL DEFAULT 0,
    dovi            INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (item_id, idx)
);

-- M5
CREATE TABLE subtitles_external (
    item_id  INTEGER NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    path     TEXT    NOT NULL,
    language TEXT    NOT NULL DEFAULT '',
    format   TEXT    NOT NULL           -- srt | vtt | ass
);
CREATE INDEX subtitles_external_item ON subtitles_external (item_id);

-- M5
CREATE TABLE progress (
    item_id      INTEGER PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    position_sec REAL    NOT NULL DEFAULT 0,
    duration_sec REAL    NOT NULL DEFAULT 0,
    watched      INTEGER NOT NULL DEFAULT 0,
    updated_at   INTEGER NOT NULL
);

-- M3: keyframe times (little-endian float64) used to cut remux segments.
CREATE TABLE keyframes (
    item_id INTEGER PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    times   BLOB
);
