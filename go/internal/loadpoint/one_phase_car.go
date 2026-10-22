package loadpoint

import (
	"math"
	"time"
)

// A one-phase car, as many plug-in hybrids are, draws on one phase of a
// three-phase charger: the order's current per phase, on that phase alone.
// The charger stays on three phases. The fuse already counts a three-phase
// order's current on every phase, which covers whichever phase the car
// uses. What changes is what the car gets: the plan counts one phase, and
// the order names the current per phase the car should take.
//
// The site meter shows it. When FTW's three-phase order rises by at least
// onePhaseStepA a phase, the meter's change from just before is watched over
// the charger's response time. One phase rising by about the order's change
// while the other two stay put is a one-phase car; all three rising is not.
// A house load does not follow FTW's orders, and the charger's own reading
// must show the car drawing what that phase shows, so a kettle cannot mark
// a car. A two-phase car moves two phases and is never marked.

const (
	onePhaseStepA = 2.0              // the smallest order rise that is a test
	onePhaseHold  = 30 * time.Second // a verdict holds this long on the meter
	// onePhaseSettle is the least response time a test waits.
	onePhaseSettle = time.Minute
)

// SitePhases is the site meter's current on each phase and when it read
// them.
type SitePhases struct {
	A  [3]float64
	At time.Time
}

// phaseTest is one order rise being watched on the meter.
type phaseTest struct {
	at, until time.Time
	baseline  [3]float64 // the meter just before the order took effect
	fromA     float64    // the order per phase before the rise
	verdict   int        // 1-3: one phase, that one; -1: three phases; 0: none
	since     time.Time  // when the meter began to show the verdict
}

// carPhases is one session's evidence. phase is 1-3 once the car is seen on
// that phase alone.
type carPhases struct {
	orderA float64 // per phase, the order the next rise is measured from
	test   *phaseTest
	phase  int
}

// phaseVerdict reads one meter reading against the baseline of a rise of
// riseA a phase. drawA is the charger's own power over one phase's voltage.
func phaseVerdict(amps, baseline [3]float64, riseA, drawA float64) int {
	var moved [3]float64
	k, big := 0, math.Inf(-1)
	for i := range amps {
		moved[i] = amps[i] - baseline[i]
		if moved[i] > big {
			k, big = i, moved[i]
		}
	}
	// The car's phase rises by the order's rise at most, and by at least
	// half of it once the charger has applied it.
	if !finite(big) || big < math.Max(onePhaseStepA, riseA/2) || big > riseA+onePhaseStepA {
		return 0
	}
	quiet, rose := 0, 0
	for i, m := range moved {
		if i == k {
			continue
		}
		if math.Abs(m) <= math.Max(1, big/4) {
			quiet++
		}
		if m >= big/2 {
			rose++
		}
	}
	switch {
	case rose == 2:
		return -1
	case quiet == 2 && finite(drawA) && drawA >= 0.6*big && drawA <= 1.5*big:
		return k + 1
	}
	return 0
}

// SetSitePhases wires the site meter's phase currents for the one-phase car
// check. nil turns the check off.
func (c *Controller) SetSitePhases(f func() (SitePhases, bool)) {
	if c != nil {
		c.sitePhases = f
	}
}

// observePhases runs this tick's one-phase car check on a fresh meter
// reading. The response time is the charger's declared power age, at least
// onePhaseSettle.
func (c *Controller) observePhases(cfg Config, sample EVSample, now time.Time) {
	site := c.siteFuse()
	if c.sitePhases == nil || !sample.Connected || site.Phases() != 3 {
		return
	}
	meter, ok := c.sitePhases()
	if !ok || meter.At.After(now.Add(time.Second)) || now.Sub(meter.At) > 10*time.Second {
		return
	}
	voltage := site.Voltage
	if !finite(voltage) || voltage <= 0 {
		voltage = 230
	}
	drawA := math.NaN()
	if !sample.PowerUnavailable && finite(sample.PowerW) {
		drawA = sample.PowerW / voltage
	}
	c.manager.observePhases(cfg.ID, meter, drawA, voltage, max(onePhaseSettle, sample.PowerWindow()), now)
}

// observePhases starts a test when the session's three-phase order rises
// by onePhaseStepA a phase, and judges it on the meter until the charger's
// response time and onePhaseHold have passed. The order may rise further
// meanwhile, as a plan catching up does; the test then expects that rise.
// A test whose order falls back is dropped. A one-phase verdict marks the
// session's car; a three-phase one clears the mark.
func (m *Manager) observePhases(id string, meter SitePhases, drawA, voltage float64, settle time.Duration, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lp, ok := m.byID[id]
	if !ok || !lp.pluggedIn {
		return
	}
	s := &lp.carPhases
	if !lp.commandedKnown || lp.commandedPhases != 3 || !finite(lp.commandedW) {
		s.test = nil
		return
	}
	orderA := math.Max(0, lp.commandedW) / (3 * voltage)
	if t := s.test; t != nil && (orderA-t.fromA < onePhaseStepA || now.After(t.until)) {
		s.test = nil
	}
	if s.test == nil {
		switch {
		case orderA-s.orderA >= onePhaseStepA:
			s.test = &phaseTest{at: now, until: now.Add(settle + onePhaseHold), baseline: meter.A, fromA: s.orderA}
		case orderA < s.orderA:
			s.orderA = orderA // A cut is the base of the next rise.
		}
		if s.test == nil {
			return
		}
		s.orderA = orderA
	}
	t := s.test
	s.orderA = max(s.orderA, orderA)
	if meter.At.Before(t.at) {
		return
	}
	if v := phaseVerdict(meter.A, t.baseline, orderA-t.fromA, drawA); v != t.verdict {
		t.verdict, t.since = v, now
	}
	if t.verdict == 0 || now.Sub(t.since) < onePhaseHold {
		return
	}
	s.phase = max(t.verdict, 0)
	s.test = nil
}

// chargerPhaseMaxA is the most current a charger offers on each phase of a
// three-phase order: its maximum over three phases, within the site fuse.
func chargerPhaseMaxA(cfg Config, site SiteFuse) float64 {
	voltage := site.Voltage
	if !finite(voltage) || voltage <= 0 {
		voltage = 230
	}
	maxA := 32.0
	if finite(cfg.MaxChargeW) && cfg.MaxChargeW > 0 {
		maxA = math.Floor(cfg.MaxChargeW/(3*voltage) + 1e-9)
	}
	if finite(site.MaxAmps) && site.MaxAmps > 0 {
		maxA = math.Min(maxA, math.Floor(site.MaxAmps))
	}
	return maxA
}

// OnePhaseCarSteps are the levels a one-phase car takes from a three-phase
// charger: 6 A up to the charger's current per phase, on one phase.
func OnePhaseCarSteps(cfg Config, site SiteFuse) []float64 {
	voltage := site.Voltage
	if !finite(voltage) || voltage <= 0 {
		voltage = 230
	}
	steps := []float64{0}
	for a := 6.0; a <= chargerPhaseMaxA(cfg, site); a++ {
		steps = append(steps, a*voltage)
	}
	return steps
}

// OnePhaseCarMaxW is the most a one-phase car takes from the charger: its
// current per phase on one phase.
func OnePhaseCarMaxW(cfg Config, site SiteFuse) float64 {
	steps := OnePhaseCarSteps(cfg, site)
	return steps[len(steps)-1]
}
