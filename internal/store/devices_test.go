package store

import (
	"context"
	"strings"
	"testing"
)

// Version 14 on a database of version 13: the devices table comes, empty, and refuses an id that is
// too short.
func TestMigration014OnAV13Database(t *testing.T) {
	old, path := openAtVersion(t, 13)
	if _, err := old.rw.Exec(`SELECT 1 FROM devices`); err == nil {
		t.Fatal("control: version 13 has devices")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := queryInt(t, s, `SELECT COUNT(*) FROM devices`); n != 0 {
		t.Fatalf("devices after the migration: %d", n)
	}
	insert := func(id string) error {
		_, err := s.rw.Exec(`INSERT INTO devices (id, created_at, last_seen) VALUES (?, 1, 1)`, id)
		return err
	}
	if err := insert("abcdefgh"); err != nil {
		t.Fatalf("control: an id of 8 was refused: %v", err)
	}
	if insert("abc") == nil {
		t.Error("an id of 3 was accepted")
	}
	if err := insert(strings.Repeat("a", 64)); err != nil {
		t.Errorf("control: an id of 64 was refused: %v", err)
	}
	if insert(strings.Repeat("b", 65)) == nil {
		t.Error("an id of 65 was accepted")
	}
}

func TestDevicesRegisterUpdateAndTouch(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	phone := Device{ID: "phone-0001", Name: "Kitchen iPad", Kind: "ipados", App: "ply", Version: "1.0"}
	tv := Device{ID: "tv-000002", Name: "Living room", Kind: "tvos", App: "ply", Version: "1.0"}
	if err := s.PutDevice(ctx, phone, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDevice(ctx, tv, 200); err != nil {
		t.Fatal(err)
	}
	list, err := s.Devices(ctx)
	if err != nil || len(list) != 2 || list[0].ID != tv.ID || list[1].ID != phone.ID {
		t.Fatalf("devices = %+v, %v: want both, the most recently seen first", list, err)
	}

	// The device says new things of itself: all of them are kept, it was seen then, and its first
	// registration stays.
	phone = Device{ID: phone.ID, Name: "Study iPad", Kind: "ios", App: "ply-beta", Version: "1.1"}
	if err := s.PutDevice(ctx, phone, 300); err != nil {
		t.Fatal(err)
	}
	list, _ = s.Devices(ctx)
	if got := list[0]; got != (Device{ID: phone.ID, Name: "Study iPad", Kind: "ios", App: "ply-beta", Version: "1.1", CreatedAt: 100, LastSeen: 300}) {
		t.Fatalf("phone after it registered again = %+v", got)
	}
	// It plays.
	if err := s.TouchDevice(ctx, phone.ID, 400); err != nil {
		t.Fatal(err)
	}
	list, _ = s.Devices(ctx)
	if got := list[0]; got.ID != phone.ID || got.CreatedAt != 100 || got.LastSeen != 400 {
		t.Fatalf("phone after it played = %+v", got)
	}
	// Neither a touch nor a registration read earlier moves last_seen back.
	if err := s.TouchDevice(ctx, phone.ID, 350); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.Devices(ctx); list[0].LastSeen != 400 {
		t.Fatalf("an older touch moved last_seen to %d", list[0].LastSeen)
	}
	if err := s.PutDevice(ctx, phone, 360); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.Devices(ctx); list[0].LastSeen != 400 {
		t.Fatalf("an older registration moved last_seen to %d", list[0].LastSeen)
	}

	// A device that never registered stays unknown; a bad id is never stored.
	if err := s.TouchDevice(ctx, "never-registered", 500); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDevice(ctx, Device{ID: "no"}, 500); err == nil {
		t.Error("a device with a 2-letter id was stored")
	}
	if err := s.PutDevice(ctx, Device{ID: "has a space"}, 500); err == nil {
		t.Error("a device id with spaces was stored")
	}
	if list, _ = s.Devices(ctx); len(list) != 2 {
		t.Fatalf("devices = %+v, want still the two", list)
	}
}

func TestValidDeviceID(t *testing.T) {
	for id, want := range map[string]bool{
		"0123456789abcdef":                     true,
		"9f1c2b4e-5d6a-4c3b-8e7f-0a1b2c3d4e5f": true, // a UUID
		"abcdefg":                              false, // 7
		strings.Repeat("x", 64):                true,
		strings.Repeat("x", 65):                false,
		"abc/defgh":                            false,
		"":                                     false,
	} {
		if got := ValidDeviceID(id); got != want {
			t.Errorf("ValidDeviceID(%q) = %v, want %v", id, got, want)
		}
	}
}
