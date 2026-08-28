package webmcp

func (b *StatefulBroker) removeTerminalOrderIDLocked(id InvocationID) {
	for i, candidate := range b.terminalOrder {
		if candidate != id {
			continue
		}
		b.terminalOrder = append(b.terminalOrder[:i], b.terminalOrder[i+1:]...)
		return
	}
}
