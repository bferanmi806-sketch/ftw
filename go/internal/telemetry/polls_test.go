package telemetry

import (
	"testing"
	"time"
)

// The fleet report's driver facts: polls and failed polls over the last 24
// clock hours, all drivers together, forgetting the hour a day back.
func TestPollsLastDayCountsADayOfPollsAndErrors(t *testing.T) {
	s := NewStore()
	now := time.Date(2026, 10, 24, 9, 30, 0, 0, time.UTC)
	s.mu.Lock()
	for i := range 10 {
		s.countPollLocked(now.Add(-25*time.Hour), i < 5) // a day and an hour ago: gone
		s.countPollLocked(now.Add(-23*time.Hour), i < 2)
		s.countPollLocked(now, false)
	}
	s.mu.Unlock()
	if polls, errors := s.PollsLastDay(now); polls != 20 || errors != 2 {
		t.Fatalf("polls %d, errors %d", polls, errors)
	}
	s.RecordDriverTick("inverter")
	s.RecordDriverError("meter", "timeout")
	if polls, errors := s.PollsLastDay(time.Now()); polls < 2 || errors < 1 {
		t.Fatalf("recorded polls not counted: %d %d", polls, errors)
	}
	if polls, _ := s.PollsLastDay(now.Add(48 * time.Hour)); polls != 0 {
		t.Fatalf("two days on, %d polls remain", polls)
	}
}
