package jobs

import (
	"ply/internal/events"
	"ply/internal/tier"
)

// EventData is the payload of the queue's events on the live feed (topic jobs).
//
// job.queued, job.updated (merged, bumped), job.held (waiting for playback or
// browsing, or no longer), job.started, job.progress, job.retrying, job.done,
// job.failed and job.cancelled carry the job; queue.paused and queue.resumed carry
// nothing. snapshot, also with nothing, says the queue changed in a way its events
// do not: the feed sends its followers a snapshot.
type EventData struct {
	Job *Job `json:"job,omitempty"`
}

// TierData is the payload of a tier event (topic tier): the state of playback and
// browsing changed.
type TierData struct {
	Tier *tier.State `json:"tier"`
}

// Events is the live feed the queue publishes to.
func (q *Queue) Events() *events.Hub { return q.o.Events }

func (q *Queue) publish(typ string, j *Job) {
	if j == nil {
		q.o.Events.Publish(events.Jobs, typ, nil)
		return
	}
	q.o.Events.Publish(events.Jobs, typ, EventData{Job: j})
}
