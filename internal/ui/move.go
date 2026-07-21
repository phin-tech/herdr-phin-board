package ui

// Grab mode: `v` picks up the selected space, then j/k walk it through the
// board. Moving inside a group reorders it; moving off either end carries it
// into the neighbouring group, which is what changes its status.

// toggleGrab picks up or drops the selected space.
func (m *Model) toggleGrab() {
	if m.grabbed != "" {
		m.grabbed = ""
		m.status = ""
		return
	}
	if !m.requireSpace() {
		return
	}
	// With a filter on, a group's visible rows are only part of it, so a move
	// would reorder against rows the user cannot see.
	if m.filter != "" {
		m.status = "clear the filter before moving rows"
		return
	}
	m.grabbed = m.selected().Key
	m.status = "moving — j/k to move, across a group to change status, enter to drop"
}

// moveGrabbed shifts the held space by one position, crossing into the
// neighbouring status group when it runs off the end of its own.
func (m *Model) moveGrabbed(delta int) {
	sp := m.selected()
	if sp == nil || sp.Key != m.grabbed {
		// The held row scrolled out from under us (a workspace closed, say).
		m.grabbed = ""
		return
	}

	group := m.groups[sp.StatusID]
	idx := indexOfSpace(group, sp.Key)
	if idx < 0 {
		return
	}

	if target := idx + delta; target >= 0 && target < len(group) {
		reordered := make([]*space, len(group))
		copy(reordered, group)
		reordered[idx], reordered[target] = reordered[target], reordered[idx]
		m.applyOrder(reordered)
		m.save()
		m.rebuild()
		return
	}
	m.crossBoundary(sp, delta)
}

// crossBoundary moves a space into the adjacent status group, entering at the
// edge it came from so the motion reads as continuous.
func (m *Model) crossBoundary(sp *space, delta int) {
	next := m.statusIndex(sp.StatusID) + delta
	if next < 0 || next >= len(m.board.Statuses) {
		m.status = "already at the end of the board"
		return
	}
	dest := m.board.Statuses[next]
	sourceID := sp.StatusID

	// Build the destination group with the space inserted at the near edge:
	// moving down enters at the top, moving up enters at the bottom.
	destGroup := make([]*space, 0, len(m.groups[dest.ID])+1)
	if delta > 0 {
		destGroup = append(destGroup, sp)
		destGroup = append(destGroup, m.groups[dest.ID]...)
	} else {
		destGroup = append(destGroup, m.groups[dest.ID]...)
		destGroup = append(destGroup, sp)
	}

	m.ensureEntry(sp)
	m.board.SetStatus(sp.Key, dest.ID, sp.Label)
	sp.StatusID = dest.ID

	// A row must never disappear because it was moved into a folded group.
	if m.board.IsCollapsed(dest.ID) {
		m.board.ToggleCollapsed(dest.ID)
	}

	source := make([]*space, 0, len(m.groups[sourceID]))
	for _, s := range m.groups[sourceID] {
		if s.Key != sp.Key {
			source = append(source, s)
		}
	}

	m.applyOrder(source)
	m.applyOrder(destGroup)
	m.save()
	m.rebuild()
	m.status = sp.Label + " → " + dest.Label
}

// applyOrder writes a group's arrangement back as 1-based ranks. Neighbours
// gain entries too, which is intended: once a column is arranged by hand, its
// order should survive rather than partially re-sort itself.
func (m *Model) applyOrder(group []*space) {
	for i, sp := range group {
		m.ensureEntry(sp)
		m.board.SetOrder(sp.Key, i+1)
	}
}

func indexOfSpace(group []*space, key string) int {
	for i, sp := range group {
		if sp.Key == key {
			return i
		}
	}
	return -1
}
