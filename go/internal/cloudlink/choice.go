package cloudlink

import (
	"errors"
	"github.com/srcfl/ftw/go/protocol"
	"github.com/srcfl/ftw/go/protocol/cloudwire"
)

var ErrCloudNeedsStatistics = errors.New("cloudlink: statistics are part of FTW Cloud; turn FTW Cloud off, then statistics, to stop contributing")

// includeStatistics records the exact scope the owner read. Service approval
// still gates every report. The caller holds mu.
func (l *Link) includeStatistics(g *grantRecord, fresh bool) error {
	if fresh || g.StatsPolicy != cloudwire.StatsPolicy || len(g.StatsID) != 16 {
		id, err := randomBytes(statsIDBytes)
		if err != nil {
			return err
		}
		g.StatsID, g.StatsAtMs = id, 0
		if l.clockSynced() {
			g.StatsAtMs = l.opts.Now().UnixMilli()
		}
	}
	g.StatsPolicy = cloudwire.StatsPolicy
	g.Purposes = withPurpose(g.Purposes, protocol.CloudPurposeStats, true)
	return nil
}

func (l *Link) enableLinkChoice() error {
	if err := l.opts.KV.SaveConfig(disabledKey, "false"); err != nil {
		return err
	}
	l.disabled = false
	return nil
}

// End stops all Cloud functions, purges the home and remembers the choice so
// a paired phone or a restart cannot silently turn the link back on.
func (l *Link) End(by string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.opts.KV.SaveConfig(disabledKey, "true"); err != nil {
		return err
	}
	l.disabled = true
	// The durable off flag is now saved. Stop the live session before any
	// follow-up write can fail; load() honours this flag after a restart.
	if l.link != nil {
		rec := *l.link
		rec.Ending = true
		l.link = &rec
	}
	if l.grant != nil {
		g := *l.grant
		g.Revoked, g.By = true, by
		l.grant = &g
	}
	l.rosterDue = true
	l.signal()
	if err := l.opts.KV.SaveConfig(statsDecisionKey, ""); err != nil {
		return err
	}
	if l.link != nil {
		if err := saveJSON(l.opts.KV, linkKey, *l.link); err != nil {
			return err
		}
	}
	if l.grant != nil {
		if err := saveJSON(l.opts.KV, grantKey, *l.grant); err != nil {
			return err
		}
	}
	return nil
}

// SharesStatistics is the one statistics answer, for both anonymous daily
// reports: the fleet report and the energy report (Fredrik, 24 October 2026).
// With FTW Cloud they are part of Cloud, with no separate off switch. A Cloud
// grant under older terms sends nothing until the owner accepts the current
// ones. Without Cloud they are on unless the owner turned them off.
func SharesStatistics(kv KV) bool {
	if kv == nil {
		return false
	}
	g, err := loadJSON[grantRecord](kv, grantKey)
	if err != nil {
		return false
	}
	// Cloud turned off is off at once, before the grant record says so.
	if disabled, _ := kv.LoadConfig(disabledKey); disabled == "true" {
		g = nil
	}
	share, _ := kv.LoadConfig(statsShareKey)
	return sharesStatistics(g, share)
}

func sharesStatistics(g *grantRecord, share string) bool {
	if g != nil && !g.Revoked && !g.statisticsOnly() {
		return g.Policy == cloudwire.CloudTermsPolicy
	}
	return share == "on" || share == "default"
}

// SaidYesToStatistics reports an active yes: FTW Cloud under the current
// terms, or a local yes. A box that shares only by default has not given one,
// so its reports reach FTW Cloud only through the relay, which hides its
// address; nothing goes straight to FTW Cloud from it.
func SaidYesToStatistics(kv KV) bool {
	if kv == nil {
		return false
	}
	g, err := loadJSON[grantRecord](kv, grantKey)
	if err != nil {
		return false
	}
	if disabled, _ := kv.LoadConfig(disabledKey); disabled == "true" {
		g = nil
	}
	if g != nil && !g.Revoked && !g.statisticsOnly() {
		return g.Policy == cloudwire.CloudTermsPolicy
	}
	share, _ := kv.LoadConfig(statsShareKey)
	return share == "on"
}

// cloudRequiresStatistics reports whether the grant is Cloud under the
// current terms, where statistics have no separate off switch.
func cloudRequiresStatistics(g *grantRecord) bool {
	return g != nil && !g.Revoked && g.Policy == cloudwire.CloudTermsPolicy
}

// MigrateStatistics writes the one answer once, from what an earlier release
// stored. A local No, a fleet report switched off, or FTW Cloud turned off
// (whose screen said that stops statistics) stays off. A local yes stays yes.
// Every other box is on by default: a box without Cloud shares unless its
// owner turns it off. Until this has run, SharesStatistics reads a box
// without Cloud as off.
func MigrateStatistics(kv KV, fleetReportOff bool) error {
	if v, ok := kv.LoadConfig(statsShareKey); ok && (v == "on" || v == "off" || v == "default") {
		return nil
	}
	g, err := loadJSON[grantRecord](kv, grantKey)
	if err != nil {
		return err
	}
	decision, _ := kv.LoadConfig(statsDecisionKey)
	disabled, _ := kv.LoadConfig(disabledKey)
	answeredYes := g != nil && !g.Revoked && g.StatsPolicy == cloudwire.StatsPolicy && len(g.StatsID) == 16
	share := "default"
	switch {
	case fleetReportOff || disabled == "true" || (decision != "" && !answeredYes):
		share = "off"
	case answeredYes && g.statisticsOnly():
		share = "on"
	}
	return kv.SaveConfig(statsShareKey, share)
}

// An unset clock cannot place a choice in UTC. Start its upload window only
// once the clock is known, never by filling earlier hours after NTP arrives.
func (l *Link) startStatisticsClock() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.disabled || l.grant == nil || l.grant.Revoked || l.grant.StatsPolicy != cloudwire.StatsPolicy || l.grant.StatsAtMs != 0 || !l.clockSynced() {
		return nil
	}
	g := *l.grant
	g.StatsAtMs = l.opts.Now().UnixMilli()
	return l.storeLocked(g, "statistics start clock set")
}
