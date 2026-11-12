package store

import (
	"context"
	"maps"
	"testing"
)

// Version 16 on a database of version 15: the devices keep their rows, unpaired.
func TestMigration016OnAV15Database(t *testing.T) {
	old, path := openAtVersion(t, 15)
	if _, err := old.rw.Exec(`INSERT INTO devices (id, name, created_at, last_seen) VALUES ('phone-0001', 'iPad', 1, 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.rw.Exec(`SELECT token_hash FROM devices`); err == nil {
		t.Fatal("control: version 15 has token_hash")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err := s.Devices(context.Background())
	if err != nil || len(list) != 1 || list[0].Name != "iPad" || list[0].PairedAt != 0 {
		t.Fatalf("devices after the migration: %+v, %v; want the iPad, unpaired", list, err)
	}
}

// Pairing gives a device a token (kept as its hash), pairing again replaces it, and revoking takes
// it away; the device stays registered all along.
func TestDevicesPairAndRevoke(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	tokens := func() map[string]string {
		t.Helper()
		m, err := s.DeviceTokens(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	phone := Device{ID: "phone-0001", Name: "Kitchen iPad", Kind: "ipados", App: "ply", Version: "1.0"}
	if err := s.PutDevice(ctx, phone, 100); err != nil {
		t.Fatal(err)
	}
	if m := tokens(); len(m) != 0 {
		t.Fatalf("tokens of a registered device = %v, want none: registering is not pairing", m)
	}

	phone.Version = "1.1"
	if err := s.PairDevice(ctx, phone, "hash-a", 200); err != nil {
		t.Fatal(err)
	}
	if m := tokens(); !maps.Equal(m, map[string]string{"hash-a": phone.ID}) {
		t.Fatalf("tokens = %v", m)
	}
	list, _ := s.Devices(ctx)
	if len(list) != 1 || list[0].PairedAt != 200 || list[0].Version != "1.1" || list[0].CreatedAt != 100 {
		t.Fatalf("devices = %+v: want it paired at 200, updated, first seen at 100", list)
	}

	if err := s.PairDevice(ctx, phone, "hash-b", 300); err != nil {
		t.Fatal(err)
	}
	if m := tokens(); !maps.Equal(m, map[string]string{"hash-b": phone.ID}) {
		t.Fatalf("tokens after pairing again = %v, want the new one alone", m)
	}

	if ok, err := s.RevokeDevice(ctx, phone.ID); !ok || err != nil {
		t.Fatalf("revoke = %v, %v", ok, err)
	}
	if m := tokens(); len(m) != 0 {
		t.Fatalf("tokens after revoking = %v", m)
	}
	list, _ = s.Devices(ctx)
	if len(list) != 1 || list[0].PairedAt != 0 {
		t.Fatalf("devices after revoking = %+v: want it still there, unpaired", list)
	}
	if ok, err := s.RevokeDevice(ctx, phone.ID); ok || err != nil {
		t.Errorf("revoking again = %v, %v; want false: it was not paired", ok, err)
	}
}
