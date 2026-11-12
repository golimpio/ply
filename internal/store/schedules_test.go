package store

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// Version 15 on a database of version 14: the schedules table comes, empty, and refuses a minute of
// the day it cannot be and a weekday that is not one.
func TestMigration015OnAV14Database(t *testing.T) {
	old, path := openAtVersion(t, 14)
	if _, err := old.rw.Exec(`SELECT 1 FROM schedules`); err == nil {
		t.Fatal("control: version 14 has schedules")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := queryInt(t, s, `SELECT COUNT(*) FROM schedules`); n != 0 {
		t.Fatalf("schedules after the migration: %d", n)
	}
	insert := func(task string, at int, days string) error {
		_, err := s.rw.Exec(`INSERT INTO schedules (task, at_minute, days, updated_at) VALUES (?, ?, ?, 1)`, task, at, days)
		return err
	}
	if err := insert("scan.full", 0, ""); err != nil {
		t.Fatalf("control: a schedule at midnight was refused: %v", err)
	}
	if err := insert("scan.full2", 1439, "1,7"); err != nil {
		t.Errorf("control: a schedule at 23:59 was refused: %v", err)
	}
	if insert("x", 1440, "") == nil {
		t.Error("a minute of 1440 was accepted")
	}
	if insert("y", -1, "") == nil {
		t.Error("a minute of -1 was accepted")
	}
}

// A schedule is written and read as it is, with its days in order and once each; marking a run
// leaves the settings alone; a task with no row is not marked.
func TestSchedulesPutReadAndMark(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	gen := s.Generation()
	rec := ScheduleRecord{Task: "ratings.mdblist", Enabled: true, AtMinute: 3*60 + 30, Days: []int{5, 1, 5}, UpdatedAt: 100}
	if err := s.PutSchedule(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Schedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("schedules = %+v, want one", got)
	}
	if !slices.Equal(got[0].Days, []int{1, 5}) || got[0].AtMinute != 210 || !got[0].Enabled || got[0].Task != rec.Task {
		t.Errorf("stored %+v, want %+v with days [1 5]", got[0], rec)
	}
	if got[0].LastRun != 0 {
		t.Errorf("last run = %d, want never", got[0].LastRun)
	}
	if s.Generation() != gen {
		t.Error("a schedule changed what a list of the library shows")
	}

	if err := s.MarkScheduleRun(ctx, rec.Task, 1234); err != nil {
		t.Fatal(err)
	}
	got, err = s.Schedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].LastRun != 1234 || got[0].AtMinute != 210 || !got[0].Enabled {
		t.Errorf("after a run %+v, want lastRun 1234 and its settings kept", got[0])
	}
	if err := s.MarkScheduleRun(ctx, "scan.full", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("marking a task with no schedule = %v, want ErrNotFound", err)
	}

	// A second task, and a change of the first: the row is replaced, not added to.
	rec.Days = nil
	rec.Enabled = false
	rec.AtMinute = 0
	if err := s.PutSchedule(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSchedule(ctx, ScheduleRecord{Task: "scan.full", Enabled: true, AtMinute: 150, UpdatedAt: 200}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Schedules(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("schedules = %+v, %v; want two by task", got, err)
	}
	if got[0].Task != "ratings.mdblist" || got[0].Enabled || got[0].AtMinute != 0 || len(got[0].Days) != 0 {
		t.Errorf("replaced schedule = %+v", got[0])
	}
	if got[1].Task != "scan.full" || got[1].AtMinute != 150 || len(got[1].Days) != 0 {
		t.Errorf("second schedule = %+v", got[1])
	}
}

// A schedule the store will not keep: no task, a minute outside the day, a day outside the week, a
// negative time. None of them is written.
func TestSchedulesRefused(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	ok := ScheduleRecord{Task: "scan.full", AtMinute: 60, UpdatedAt: 1}
	for name, rec := range map[string]ScheduleRecord{
		"no task":     {AtMinute: 60},
		"minute 1440": {Task: "x", AtMinute: 1440},
		"minute -1":   {Task: "x", AtMinute: -1},
		"day 0":       {Task: "x", AtMinute: 0, Days: []int{0}},
		"day 8":       {Task: "x", AtMinute: 0, Days: []int{8}},
		"negative":    {Task: "x", AtMinute: 0, LastRun: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := s.PutSchedule(ctx, rec); !errors.Is(err, ErrInvalidSchedule) {
				t.Fatalf("PutSchedule(%+v) = %v, want ErrInvalidSchedule", rec, err)
			}
		})
	}
	if err := s.PutSchedule(ctx, ok); err != nil { // control: the record all the refusals were made from
		t.Fatal(err)
	}
	if got, _ := s.Schedules(ctx); len(got) != 1 {
		t.Errorf("a refused schedule was written: %+v", got)
	}
}
