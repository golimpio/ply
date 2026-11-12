package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
)

// Device is a client of ply as it registered itself (docs/M11-CLIENTS.md, "Devices").
type Device struct {
	ID        string
	Name      string
	Kind      string // web, ios, ipados, tvos, android, androidtv, …: an open list
	App       string
	Version   string
	CreatedAt int64 // unix seconds
	LastSeen  int64 // unix seconds: the last registration or play
	PairedAt  int64 // unix seconds; 0: not paired (or revoked)
}

var reDeviceID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// ValidDeviceID reports whether id can be a device's id. One that cannot is ignored, never stored.
func ValidDeviceID(id string) bool { return reDeviceID.MatchString(id) }

// PutDevice registers d, or updates what it says of itself, seen at now. Its first registration
// stays its CreatedAt. No list shows devices: it leaves Generation alone. The device is waiting for
// the answer: it goes ahead of the background writes.
func (s *Store) PutDevice(ctx context.Context, d Device, now int64) error {
	if !ValidDeviceID(d.ID) {
		return fmt.Errorf("store: device id %q", d.ID)
	}
	return s.writeQuiet(ctx, prioHigh, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO devices (id, name, kind, app, version, created_at, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET name = excluded.name, kind = excluded.kind, app = excluded.app,
				version = excluded.version, last_seen = max(last_seen, excluded.last_seen)`,
			d.ID, d.Name, d.Kind, d.App, d.Version, now, now)
		return err
	})
}

// TouchDevice records that the device id was seen at now (it started a play). A device that never
// registered is left unknown: nothing is written for it.
func (s *Store) TouchDevice(ctx context.Context, id string, now int64) error {
	if !ValidDeviceID(id) {
		return nil
	}
	return s.writeQuiet(ctx, prioLow, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE devices SET last_seen = ? WHERE id = ? AND last_seen < ?`, now, id, now)
		return err
	})
}

// Devices lists the registered devices, the most recently seen first.
func (s *Store) Devices(ctx context.Context) ([]Device, error) {
	var out []Device
	err := s.snapshot(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, `SELECT id, name, kind, app, version, created_at, last_seen,
				CASE WHEN token_hash IS NULL THEN 0 ELSE paired_at END
			FROM devices ORDER BY last_seen DESC, id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Device
			if err := rows.Scan(&d.ID, &d.Name, &d.Kind, &d.App, &d.Version, &d.CreatedAt, &d.LastSeen, &d.PairedAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// PairDevice registers d as PutDevice does and gives it the token whose SHA-256 is tokenHash, in
// place of any it had (docs/M11-CLIENTS.md, "Pairing").
func (s *Store) PairDevice(ctx context.Context, d Device, tokenHash string, now int64) error {
	if !ValidDeviceID(d.ID) {
		return fmt.Errorf("store: device id %q", d.ID)
	}
	return s.writeQuiet(ctx, prioHigh, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO devices (id, name, kind, app, version, created_at, last_seen, token_hash, paired_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET name = excluded.name, kind = excluded.kind, app = excluded.app,
				version = excluded.version, last_seen = max(last_seen, excluded.last_seen),
				token_hash = excluded.token_hash, paired_at = excluded.paired_at`,
			d.ID, d.Name, d.Kind, d.App, d.Version, now, now, tokenHash, now)
		return err
	})
}

// RevokeDevice takes the token of device id away. It reports whether the device was paired.
func (s *Store) RevokeDevice(ctx context.Context, id string) (bool, error) {
	var n int64
	err := s.writeQuiet(ctx, prioHigh, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE devices SET token_hash = NULL, paired_at = 0
			WHERE id = ? AND token_hash IS NOT NULL`, id)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n > 0, err
}

// DeviceTokens maps the SHA-256 (hex) of each paired device's token to the device's id.
func (s *Store) DeviceTokens(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := s.snapshot(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, `SELECT token_hash, id FROM devices WHERE token_hash IS NOT NULL`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var hash, id string
			if err := rows.Scan(&hash, &id); err != nil {
				return err
			}
			out[hash] = id
		}
		return rows.Err()
	})
	return out, err
}
