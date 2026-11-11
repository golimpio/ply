-- The devices that use ply (docs/M11-CLIENTS.md, "Devices"): the id each client makes and keeps, what
-- it says it is, and when it was first and last seen (unix seconds; last_seen moves on registration
-- and when the device starts a play, never on every request).
CREATE TABLE devices (
    id         TEXT    PRIMARY KEY CHECK (length(id) BETWEEN 8 AND 64),
    name       TEXT    NOT NULL DEFAULT '',
    kind       TEXT    NOT NULL DEFAULT '',
    app        TEXT    NOT NULL DEFAULT '',
    version    TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL
);
