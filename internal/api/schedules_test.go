package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"ply/internal/schedule"
)

// fakeScheduler is the schedules as the API sees them.
type fakeScheduler struct {
	entries []schedule.Entry
	listErr error
	setErr  error
	runErr  error
	gotTask string
	gotEdit schedule.Edit
	ran     string
}

func (f *fakeScheduler) List(context.Context) ([]schedule.Entry, error) {
	return f.entries, f.listErr
}

func (f *fakeScheduler) Set(_ context.Context, task string, edit schedule.Edit) (schedule.Entry, error) {
	f.gotTask, f.gotEdit = task, edit
	if f.setErr != nil {
		return schedule.Entry{}, f.setErr
	}
	e := schedule.Entry{Task: task, Title: "Scan the library", Detail: "Walks the folders.", At: schedule.MustClock("02:30"),
		Days: []int{}, Missing: []string{}, Warnings: []string{}}
	if edit.Enabled != nil {
		e.Enabled = *edit.Enabled
	}
	if edit.At != nil {
		e.At = *edit.At
	}
	if edit.Days != nil {
		e.Days = *edit.Days
	}
	return e, nil
}

func (f *fakeScheduler) RunNow(_ context.Context, task string) error {
	f.ran = task
	return f.runErr
}

func newScheduleEnv(t *testing.T, fs *fakeScheduler) *menv {
	t.Helper()
	return newMetaEnvWith(t, apiToken, func(_ *menv, o *Options) { o.Schedules = fs })
}

func TestSchedulesList(t *testing.T) {
	last := time.Date(2026, 11, 8, 2, 30, 0, 0, time.UTC)
	next := time.Date(2026, 11, 9, 2, 30, 0, 0, time.UTC)
	fs := &fakeScheduler{entries: []schedule.Entry{
		{Task: "scan.full", Title: "Scan the library", Detail: "Walks the folders.", Enabled: true,
			At: schedule.MustClock("02:30"), Days: []int{}, LastRun: &last, Next: &next, Missing: []string{}, Warnings: []string{}},
		{Task: "ratings.mdblist", Title: "Ratings", Detail: "Rated.", Enabled: true, At: schedule.MustClock("03:30"),
			Days: []int{1, 3}, Missing: []string{"MDBList key"}, Warnings: []string{}, Blocked: "The MDBList key is not set."},
	}}
	m := newScheduleEnv(t, fs)
	rec := m.get("/api/schedules")
	wantStatus(t, rec, 200)
	var got []schedule.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("answer: %v: %s", err, rec.Body.String())
	}
	if len(got) != 2 || got[0].Task != "scan.full" || got[1].Task != "ratings.mdblist" {
		t.Fatalf("schedules = %+v", got)
	}
	if got[0].At.String() != "02:30" || got[0].Next == nil || !got[0].Next.Equal(next) || got[0].LastRun == nil || !got[0].LastRun.Equal(last) {
		t.Errorf("the scan's times = %+v, want at 02:30, last %v and next %v", got[0], last, next)
	}
	if got[1].At.String() != "03:30" || len(got[1].Days) != 2 || got[1].Blocked == "" || len(got[1].Missing) != 1 {
		t.Errorf("the ratings schedule = %+v", got[1])
	}
	if got[1].Next != nil || got[1].LastRun != nil {
		t.Errorf("a schedule blocked and never run has a time it did not have: %+v", got[1])
	}
	// The page reads these as lists: null would be a different thing in TypeScript.
	for _, want := range []string{`"days":[]`, `"missing":[]`, `"warnings":[]`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the answer has no %s: %s", want, rec.Body.String())
		}
	}
}

func TestSchedulesPut(t *testing.T) {
	fs := &fakeScheduler{}
	m := newScheduleEnv(t, fs)
	rec := m.do(http.MethodPut, "/api/schedules/scan.full", `{"enabled": false, "at": "04:15", "days": [5, 1, 5]}`)
	wantStatus(t, rec, 200)
	if fs.gotTask != "scan.full" || fs.gotEdit.Enabled == nil || *fs.gotEdit.Enabled || fs.gotEdit.At == nil ||
		fs.gotEdit.At.String() != "04:15" || fs.gotEdit.Days == nil || len(*fs.gotEdit.Days) != 3 {
		t.Fatalf("the server was given task %q and %+v", fs.gotTask, fs.gotEdit)
	}
	var got schedule.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if got.Task != "scan.full" || got.Enabled || got.At.String() != "04:15" || len(got.Days) != 3 {
		t.Errorf("answer = %+v, want the change", got)
	}

	// One field at a time: the others stay as they are.
	rec = m.do(http.MethodPut, "/api/schedules/scan.full", `{"enabled": true}`)
	wantStatus(t, rec, 200)
	if fs.gotEdit.At != nil || fs.gotEdit.Days != nil || fs.gotEdit.Enabled == nil || !*fs.gotEdit.Enabled {
		t.Errorf("a change of the switch carried the others: %+v", fs.gotEdit)
	}
}

func TestSchedulesPutRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		setErr error
		body   string
		want   int
	}{
		"unknown task": {schedule.ErrUnknownTask, `{"enabled": true}`, 404},
		"bad day":      {fmt.Errorf("%w: 9 is not a day of the week", schedule.ErrInvalid), `{"days": [9]}`, 400},
		"no change":    {fmt.Errorf("%w: nothing to change", schedule.ErrInvalid), `{}`, 400},
		"bad time":     {schedule.ErrInvalid, `{"at": "25:00"}`, 400},
		"not json":     {nil, `{`, 400},
		"other":        {errors.New("database trouble"), `{"enabled": true}`, 500},
	} {
		t.Run(name, func(t *testing.T) {
			fs := &fakeScheduler{setErr: tc.setErr}
			m := newScheduleEnv(t, fs)
			wantStatus(t, m.do(http.MethodPut, "/api/schedules/scan.full", tc.body), tc.want)
		})
	}
	// No scheduler at all (a server of another version, or one building): 503.
	bare := newMetaEnvWith(t, apiToken, func(_ *menv, o *Options) { o.Schedules = nil })
	wantStatus(t, bare.do(http.MethodPut, "/api/schedules/scan.full", `{"enabled": true}`), 503)
	wantStatus(t, bare.get("/api/schedules"), 503)
}

func TestSchedulesRunNow(t *testing.T) {
	fs := &fakeScheduler{}
	m := newScheduleEnv(t, fs)
	rec := m.do(http.MethodPost, "/api/schedules/scan.full/run", "")
	wantStatus(t, rec, 204)
	if fs.ran != "scan.full" {
		t.Errorf("ran %q, want scan.full", fs.ran)
	}

	for name, tc := range map[string]struct {
		err  error
		want int
		body string
	}{
		"unknown": {schedule.ErrUnknownTask, 404, ""},
		"busy":    {schedule.ErrBusy, 409, "already queued or running"},
		"refused": {&schedule.Refused{Reason: "The MDBList key is not set.", Missing: []string{"MDBList key"}}, 409, "MDBList key"},
		"other":   {errors.New("queue trouble"), 500, ""},
	} {
		t.Run(name, func(t *testing.T) {
			fs := &fakeScheduler{runErr: tc.err}
			m := newScheduleEnv(t, fs)
			rec := m.do(http.MethodPost, "/api/schedules/ratings.mdblist/run", "")
			wantStatus(t, rec, tc.want)
			if tc.body != "" && !strings.Contains(rec.Body.String(), tc.body) {
				t.Errorf("the answer does not say %q: %s", tc.body, rec.Body.String())
			}
		})
	}
}
