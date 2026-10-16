package cars

import (
	"testing"
	"time"

	"github.com/srcfl/ftw/go/internal/telemetry"
	"github.com/srcfl/ftw/go/protocol"
)

// twoCars is Fredrik's box with a second car: the Tesla reads through Home
// Assistant (tesla-ha) and the Volvo through its cloud. With two cars rule 3
// cannot answer, so only the cars' own word or a tap names one.
func twoCars(t *testing.T) (*harness, protocol.Car, protocol.Car) {
	t.Helper()
	h := newHarness(t, homeConfig(), nil, nil)
	h.start()
	tesla := h.only()
	if _, err := h.rt.UpdateCar(tesla.ID, Update{Sources: &[]string{"tesla-ha"}}); err != nil {
		t.Fatal(err)
	}
	volvo := h.add(protocol.Car{Name: "Volvo", CapacityWh: 40000, Sources: []string{"volvo-cloud"}})
	return h, tesla, volvo
}

// says is a vehicle driver's report of its car's cable and place, made now.
func (h *harness) says(driver string, plugged bool, since time.Time, atSite *bool) {
	h.obs[driver] = telemetry.VehicleObservation{Driver: driver, SoC: .4, At: h.now,
		Plugged: boolp(plugged), PluggedSince: since, AtSite: atSite, ReportedAt: h.now}
}

// A car whose source reports minutes after the plug-in still names itself
// when its own clock puts the cable within a minute of it, even as the
// source's first report since the box started.
func TestTheCarsOwnClockSaysWhenItsCableWentIn(t *testing.T) {
	h, tesla, _ := twoCars(t)
	h.plug("easee", "s1")
	plugAt := h.now
	h.tick(90 * time.Second)
	if st := h.state("easee"); st.CarID != "" || len(h.asked) != 1 {
		t.Fatalf("with no word from a car the box asks: %+v asked %d", st, len(h.asked))
	}
	h.says("tesla-ha", true, plugAt.Add(5*time.Second), boolp(true))
	h.tick(time.Second)
	if st := h.state("easee"); st.CarID != tesla.ID || st.CarIdentifiedBy != protocol.CarIdentifiedByCar {
		t.Fatalf("the car's own clock: %+v", st)
	}
}

// Two cars that both say they were plugged in within the minute are no
// answer, whether or not they know where they are: the box asks.
func TestTwoCarsClaimingThePlugInIsNoAnswer(t *testing.T) {
	for _, atSite := range []*bool{boolp(true), nil} {
		h, _, _ := twoCars(t)
		h.says("tesla-ha", false, time.Time{}, atSite)
		h.says("volvo-cloud", false, time.Time{}, atSite)
		h.tick(time.Second)
		h.plug("easee", "s1")
		h.says("tesla-ha", true, h.now.Add(10*time.Second), atSite)
		h.says("volvo-cloud", true, h.now.Add(20*time.Second), atSite)
		h.tick(20 * time.Second)
		if st := h.state("easee"); st.CarID != "" || st.CarIdentifiedBy == protocol.CarIdentifiedByCar {
			t.Fatalf("at_site %v: two claims named a car: %+v", atSite, st)
		}
		h.tick(time.Minute)
		if st := h.state("easee"); st.CarIdentifiedBy != protocol.CarIdentifiedByAsking || len(h.asked) != 1 {
			t.Fatalf("at_site %v: the box should ask: %+v asked %d", atSite, st, len(h.asked))
		}
	}
}

// A car that says it is somewhere else is never named, even when it says
// its cable went in at the same minute and it is the only car.
func TestACarAwayIsNeverNamed(t *testing.T) {
	h := newHarness(t, homeConfig(), nil, nil)
	h.start()
	car := h.only()
	if _, err := h.rt.UpdateCar(car.ID, Update{Sources: &[]string{"tesla-ha"}}); err != nil {
		t.Fatal(err)
	}
	h.says("tesla-ha", false, time.Time{}, boolp(false))
	h.tick(time.Second)
	h.plug("easee", "s1")
	h.says("tesla-ha", true, h.now.Add(5*time.Second), boolp(false))
	for i := 0; i < 4; i++ {
		h.tick(30 * time.Second)
		if st := h.state("easee"); st.CarID == car.ID {
			t.Fatalf("a car away was named: %+v", st)
		}
	}
	if st := h.state("easee"); st.CarIdentifiedBy != protocol.CarIdentifiedByNone {
		t.Fatalf("the only car is away, so this is a visitor: %+v", st)
	}
}

// A car that does not know where it is counts only by the minute: a cable
// that went in five minutes before this plug-in is another plug-in.
func TestACarThatDoesNotKnowWhereItIsCountsOnlyByTheMinute(t *testing.T) {
	h, tesla, _ := twoCars(t)
	h.plug("easee", "s1")
	h.says("tesla-ha", true, h.now.Add(-5*time.Minute), nil)
	h.tick(90 * time.Second)
	if st := h.state("easee"); st.CarID != "" || st.CarIdentifiedBy != protocol.CarIdentifiedByAsking {
		t.Fatalf("an old plug-in named the car: %+v", st)
	}
	h.unplug("easee")
	h.says("tesla-ha", false, h.now, nil)
	h.tick(time.Second)
	h.plug("easee", "s2")
	h.says("tesla-ha", true, h.now.Add(15*time.Second), nil)
	h.tick(20 * time.Second)
	if st := h.state("easee"); st.CarID != tesla.ID || st.CarIdentifiedBy != protocol.CarIdentifiedByCar {
		t.Fatalf("a fresh plug-in with no place: %+v", st)
	}
}

// A source that replays a reading from before the plug-in has not seen it:
// its old "cable out" or "elsewhere" does not make the only car a visitor.
// A reading made after the plug-in does.
func TestAReadingFromBeforeThePlugInSaysNothingAboutIt(t *testing.T) {
	h := newHarness(t, homeConfig(), nil, nil)
	h.start()
	car := h.only()
	if _, err := h.rt.UpdateCar(car.ID, Update{Sources: &[]string{"tesla-ha"}}); err != nil {
		t.Fatal(err)
	}
	readAt := h.now
	h.tick(10 * time.Minute)
	h.plug("easee", "s1")
	// Home Assistant has not read the car since it came home.
	h.obs["tesla-ha"] = telemetry.VehicleObservation{Driver: "tesla-ha", SoC: .4, At: readAt,
		Plugged: boolp(false), AtSite: boolp(false), Stale: true, ReportedAt: h.now.Add(10 * time.Second)}
	h.tick(10 * time.Second)
	if st := h.state("easee"); st.CarID != car.ID || st.CarIdentifiedBy != protocol.CarIdentifiedByOnly {
		t.Fatalf("an old reading turned the only car into a visitor: %+v", st)
	}
	// Then it reads the car, 20 s after the plug-in: elsewhere.
	h.obs["tesla-ha"] = telemetry.VehicleObservation{Driver: "tesla-ha", SoC: .4, At: h.now.Add(10 * time.Second),
		Plugged: boolp(false), AtSite: boolp(false), ReportedAt: h.now.Add(10 * time.Second)}
	h.tick(10 * time.Second)
	if st := h.state("easee"); st.CarID != "" || st.CarIdentifiedBy != protocol.CarIdentifiedByNone {
		t.Fatalf("a reading after the plug-in says the car is elsewhere: %+v", st)
	}
}
