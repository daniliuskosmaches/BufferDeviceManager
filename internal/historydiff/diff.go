// Package historydiff compares snapshots of the Windows clipboard history.
package historydiff

// Changes returns IDs newly present and no longer present. Order changes alone
// are not additions or deletions. Returned IDs follow their snapshot order.
func Changes(previous, current []string) (added, removed []string) {
	before := make(map[string]bool, len(previous))
	after := make(map[string]bool, len(current))
	for _, id := range previous {
		before[id] = true
	}
	for _, id := range current {
		if !before[id] && !after[id] {
			added = append(added, id)
		}
		after[id] = true
	}
	for _, id := range previous {
		if !after[id] {
			removed = append(removed, id)
			after[id] = true
		}
	}
	return added, removed
}
