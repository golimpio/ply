-- Pairing (docs/M11-CLIENTS.md, "Pairing"): a paired device's own token, kept as its SHA-256 (hex), and
-- when it was paired (unix seconds). No token: the device is not paired, or was revoked.
ALTER TABLE devices ADD COLUMN token_hash TEXT;
ALTER TABLE devices ADD COLUMN paired_at INTEGER NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX devices_token_hash ON devices (token_hash) WHERE token_hash IS NOT NULL;
