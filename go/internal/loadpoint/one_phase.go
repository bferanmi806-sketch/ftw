package loadpoint

import (
	"math"
	"time"
)

// A charger told to use three phases may still draw on one: an Easee whose
// phase mode stays on 1p, or a driver that resolves auto to one phase at a
// split Core does not use. The fuse limit reads the site meter and holds
// either way. The plan does not: it counts the order's watts, and the car
// takes what one phase gives. The site meter shows where the draw lands:
// all of the charger's current on one phase, less than a third of it on the
// other two. Two minutes of aligned readings say so for the session, and the
// rate the charger delivered becomes the plan's rate. Two minutes with every
// phase carrying its third end it.

const (
	// onePhaseMinW keeps a third of the draw clear of a small house load.
	onePhaseMinW    = 2000.0
	onePhaseMarginA = 1.0
	onePhaseConfirm = 2 * time.Minute
	// onePhaseAlign is how far apart the charger's and the meter's readings
	// may be, and how old the meter's may be.
	onePhaseAlign = 10 * time.Second
	// onePhaseGap ends a run of readings that has a hole in it.
	onePhaseGap = 30 * time.Second
)

// SitePhases is the site meter's current on each phase, as magnitudes, and
// when it read them.
type SitePhases struct {
	A  [3]float64
	At time.Time
}

// onePhaseDraw says where a charger's draw lands: the phase (1-3) that
// carries all of it while the other two carry less than a third, -1 when
// every phase carries at least a third, or 0 when the meter cannot tell.
func onePhaseDraw(drawW, voltage float64, amps [3]float64) int {
	if !finite(drawW) || drawW < onePhaseMinW || !finite(voltage) || voltage <= 0 {
		return 0
	}
	full, third := drawW/voltage-onePhaseMarginA, drawW/(3*voltage)-onePhaseMarginA
	phase, below := 0, 0
	for i, a := range amps {
		if !finite(a) {
			return 0
		}
		a = math.Abs(a)
		if a >= full {
			phase = i + 1
		}
		if a < third {
			below++
		}
	}
	switch {
	case phase > 0 && below == 2:
		return phase
	case below == 0:
		return -1
	}
	return 0
}

// onePhaseState is one session's evidence. rateW is the most the charger
// delivered on one phase and orderW the order in force when it did.
type onePhaseState struct {
	since, at           time.Time // the current run of one-phase readings
	clearSince, clearAt time.Time // the current run of three-phase readings
	phase               int
	rateW, orderW       float64
}

// SetSitePhases wires the site meter's phase currents for the one-phase
// check. nil turns the check off.
func (c *Controller) SetSitePhases(f func() (SitePhases, bool)) {
	if c != nil {
		c.sitePhases = f
	}
}

// observeOnePhase compares a charger's fresh draw under a three-phase order
// with the site meter's phases read at the same time.
func (c *Controller) observeOnePhase(cfg Config, sample EVSample, now time.Time) {
	if c.sitePhases == nil || !sample.Connected || sample.PowerUnavailable || sample.PowerAt.IsZero() {
		return
	}
	site := c.siteFuse()
	st, ok := c.manager.State(cfg.ID)
	if !ok || !st.CommandedKnown || st.CommandedW <= 0 || st.CommandedPhases != 3 || site.Phases() != 3 {
		return
	}
	meter, ok := c.sitePhases()
	if !ok || now.Sub(meter.At) > onePhaseAlign || meter.At.Sub(sample.PowerAt).Abs() > onePhaseAlign {
		return
	}
	voltage := site.Voltage
	if !finite(voltage) || voltage <= 0 {
		voltage = 230
	}
	c.manager.observeOnePhase(cfg.ID, onePhaseDraw(sample.PowerW, voltage, meter.A), sample.PowerW, st.CommandedW, now)
}

func (m *Manager) observeOnePhase(id string, phase int, drawW, orderW float64, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lp, ok := m.byID[id]
	if !ok || !lp.pluggedIn {
		return
	}
	s := &lp.onePhase
	switch {
	case phase > 0:
		s.clearSince, s.clearAt = time.Time{}, time.Time{}
		if s.since.IsZero() || now.Sub(s.at) > onePhaseGap {
			s.since = now
		}
		s.at = now
		if now.Sub(s.since) < onePhaseConfirm {
			return
		}
		if s.phase == 0 || drawW > s.rateW {
			s.rateW, s.orderW = drawW, orderW
		}
		s.phase = phase
	case phase < 0:
		s.since, s.at = time.Time{}, time.Time{}
		if s.phase == 0 {
			return
		}
		if s.clearSince.IsZero() || now.Sub(s.clearAt) > onePhaseGap {
			s.clearSince = now
		}
		s.clearAt = now
		if now.Sub(s.clearSince) >= onePhaseConfirm {
			*s = onePhaseState{}
		}
	}
}
