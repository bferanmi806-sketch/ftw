package appproto

import (
	"sync"
	"time"
)

// PlanSettle is how long the feed waits after the planner's first word
// before it pushes. A change that replans twice in that time reaches the
// phone as one plan.
const PlanSettle = 250 * time.Millisecond

// PlanFeed pushes the plan to every streaming session the moment the box's
// plan changes, so a phone never shows a plan the box has replaced.
//
// The planner calls Changed when it publishes a plan and when its newest
// replan ends without one. Nothing here runs on a timer of its own: the only
// wait is the settle after a change. One feed serves the whole box; each
// handler joins it when built and leaves when closed.
type PlanFeed struct {
	settle time.Duration
	// newer reports that a newer plan is being made. A push then would show
	// a plan that is about to go, and the newer one calls Changed itself.
	newer func() bool

	mu      sync.Mutex
	members map[*Handler]struct{}
	armed   bool
}

// NewPlanFeed builds the box's feed. newer may be nil.
func NewPlanFeed(settle time.Duration, newer func() bool) *PlanFeed {
	return &PlanFeed{settle: settle, newer: newer, members: map[*Handler]struct{}{}}
}

// Changed is the planner's word that what a plan reader sees has moved.
// Calls inside the settle after the first are the same push.
func (f *PlanFeed) Changed() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.armed {
		return
	}
	f.armed = true
	time.AfterFunc(f.settle, f.push)
}

func (f *PlanFeed) push() {
	f.mu.Lock()
	f.armed = false
	f.mu.Unlock()
	if f.newer != nil && f.newer() {
		return
	}
	f.mu.Lock()
	members := make([]*Handler, 0, len(f.members))
	for h := range f.members {
		members = append(members, h)
	}
	f.mu.Unlock()
	for _, h := range members {
		h.pushPlan()
	}
}

func (f *PlanFeed) join(h *Handler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[h] = struct{}{}
}

func (f *PlanFeed) leave(h *Handler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.members, h)
}

// pushPlan sends the plan unasked to a phone that streams and may read it.
// The role is read as it stands now, the way every privileged request reads
// it: a phone demoted to a viewer stops getting plans at the next one.
func (h *Handler) pushPlan() {
	h.mu.Lock()
	streaming := h.subscribed
	h.mu.Unlock()
	if !streaming || h.cfg.Info.Boot() != nil {
		return
	}
	caller, ok := h.liveCaller()
	if !ok || !caller.Scopes.Has(ScopePlanRead) {
		return
	}
	// A failed send is the session's carrier going away; the carrier ends
	// the session, and this handler leaves the feed when it closes.
	if err := h.sendPlan(nil); err != nil {
		h.log.Debug("plan push not sent", "err", err)
	}
}
