package notifications

// NotifyCloudGrant tells every owner that FTW Cloud was turned on or off,
// with the catalogue's sentence for kind. The rule gates it like any other,
// but a box whose stored rules predate these kinds still sends it: a missing
// rule reads as the default, which is on.
func (s *Service) NotifyCloudGrant(kind string) {
	if s == nil || (kind != PushCloudGranted && kind != PushCloudRevoked) {
		return
	}
	s.mu.Lock()
	if s.cfg == nil || !s.cfg.Enabled {
		s.mu.Unlock()
		return
	}
	rule, ok := findRule(s.cfg.Events, kind)
	if !ok {
		rule, _ = findRule(DefaultRules(), kind)
	}
	prio := rule.Priority
	if prio == 0 {
		prio = s.cfg.DefaultPriority
	}
	s.mu.Unlock()
	if !rule.Enabled {
		return
	}

	title, body, err := RenderPush(kind, nil)
	if err != nil {
		s.bumpFailed()
		s.emitDispatched(kind, "", Message{Priority: prio}, "failed", err.Error())
		return
	}
	s.deliver(kind, "", Message{Kind: kind, Title: title, Body: body, Priority: prio, Tags: splitTags(rule.Tags)})
}
