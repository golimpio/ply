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
		rows, err := q.QueryContext(ctx, `SELECT id, name, kind, app, version, created_at, last_seen
			FROM devices ORDER BY last_seen DESC, id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Device
			if err := rows.Scan(&d.ID, &d.Name, &d.Kind, &d.App, &d.Version, &d.CreatedAt, &d.LastSeen); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}
