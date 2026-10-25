package mpc

import (
	"fmt"
	"math"
	"time"
)

// ImportCeiling is the owner's cap on each local clock hour's mean import as
// the planner gets it from Core's live import hour
// (control.PlanningImportCeiling).
type ImportCeiling struct {
	W         float64   // the owner's ceiling; 0 = off
	MeanW     float64   // the mean import a planned hour may hold
	HourStart time.Time // the local clock hour in progress
	ElapsedWh float64   // what that hour has used, as Core's live limit counts it
}

// ImportHour is one local clock hour the plan spans and the import it had
// used before the plan starts. Core expands the hours from its own clock, so
// the planner needs no calendar: a local clock hour lasts an hour on the 23-
// and 25-hour days too.
type ImportHour struct {
	StartMs, EndMs  int64
	ElapsedImportWh float64
}

const hourMs = int64(time.Hour / time.Millisecond)

// importHours lists the clock hours the slots overlap from their execution
// start: the hour in progress with what it has used, each later hour unused.
// The last may run past the horizon.
func importHours(slots []Slot, c ImportCeiling) []ImportHour {
	if !(c.W > 0) || !finite(c.MeanW) || c.MeanW < 0 || !finite(c.ElapsedWh) || c.HourStart.IsZero() || len(slots) == 0 {
		return nil
	}
	from := slots[0].ExecutionStart()
	end, ok := slotEndMs(slots[len(slots)-1])
	if !ok {
		return nil
	}
	var out []ImportHour
	for start := c.HourStart.UnixMilli(); start < end; start += hourMs {
		if start+hourMs <= from {
			continue
		}
		h := ImportHour{StartMs: start, EndMs: start + hourMs}
		if start == c.HourStart.UnixMilli() {
			h.ElapsedImportWh = c.ElapsedWh
		}
		out = append(out, h)
	}
	return out
}

// importLeftWh is what the plan may import in h: the hour's mean over its
// length, less what it had used. Negative once overrun.
func importLeftWh(h ImportHour, meanW float64) float64 {
	return meanW*float64(h.EndMs-h.StartMs)/float64(hourMs) - h.ElapsedImportWh
}

func importHourOverlapH(s Slot, h ImportHour) float64 {
	end, ok := slotEndMs(s)
	if !ok {
		return 0
	}
	return float64(max(0, min(end, h.EndMs)-max(s.ExecutionStart(), h.StartMs))) / float64(hourMs)
}

// importHourCapsW spreads each hour's import left over its slots: the most a
// slot's mean import may be. Each slot keeps its own house import, so doing
// nothing stays feasible, and they share the room left evenly, never above
// the hour's mean: Core's live limit starts each hour at the mean, so it
// lets an even spread through. Nil when no hour applies.
func importHourCapsW(slots []Slot, p Params) []float64 {
	if len(p.ImportHours) == 0 {
		return nil
	}
	caps := make([]float64, len(slots))
	for i := range caps {
		caps[i] = math.Inf(1)
	}
	for _, h := range p.ImportHours {
		var spanH, houseWh float64
		for _, s := range slots {
			if ov := importHourOverlapH(s, h); ov > 0 {
				spanH += ov
				houseWh += max(0, s.LoadW+s.PVW) * ov
			}
		}
		if spanH <= 0 {
			continue
		}
		leftWh := max(0, importLeftWh(h, p.ImportMeanW))
		meanW, roomW := leftWh/spanH, max(0, leftWh-houseWh)/spanH
		for i, s := range slots {
			if importHourOverlapH(s, h) > 0 {
				house := max(0, s.LoadW+s.PVW)
				caps[i] = min(caps[i], max(house, min(house+roomW, meanW)))
			}
		}
	}
	return caps
}

// evAverageRoomW is the most mean EV power a slot holds under capW of mean
// import, with baseW the rest of its grid flow and the car pulsing at
// peakW. A pulse imports while it runs even when the slot exports between.
func evAverageRoomW(capW, baseW, peakW float64) float64 {
	off, on := max(0, baseW), max(0, baseW+peakW)
	if on <= off {
		return peakW
	}
	return max(0, min(peakW, (capW-off)*peakW/(on-off)))
}

// validateImportHours checks the plan's import in each clock hour against
// what the hour has left. The house's own import never fails it: Core cannot
// cut the house, only the battery and the cars.
func validateImportHours(slots []Slot, p Params, actions []Action) error {
	for _, h := range p.ImportHours {
		var spanH, importWh, houseWh float64
		for i, s := range slots {
			ov := importHourOverlapH(s, h)
			if ov <= 0 {
				continue
			}
			spanH += ov
			pulseGridW(actions[i], func(share, gridW float64) { importWh += share * max(0, gridW) * ov })
			houseWh += max(0, s.LoadW+s.PVW) * ov
		}
		leftWh := importLeftWh(h, p.ImportMeanW)
		if importWh > max(leftWh, houseWh)+solverGridLimitToleranceW*spanH {
			return fmt.Errorf("clock hour from %s imports %.0f Wh, over the %.0f Wh it has left",
				time.UnixMilli(h.StartMs).UTC().Format(time.RFC3339), importWh, leftWh)
		}
	}
	return nil
}
