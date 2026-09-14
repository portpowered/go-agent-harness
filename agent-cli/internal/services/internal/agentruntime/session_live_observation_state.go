package agentruntime

func (s *observedSession) markDone() {
	s.once.Do(s.closeDone)
}
