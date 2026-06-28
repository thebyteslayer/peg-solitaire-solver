package main

// StateSet is a memory-compact open-addressing hash set of States.
// Each slot is 8 bytes (one uint64) vs ~50+ bytes/entry for a Go map, so it
// holds several times more states in the same RAM. The all-zero State (an
// empty board, which never occurs as a stored position) is the empty sentinel.
type StateSet struct {
	slots []State
	mask  uint64
	count int
}

func newStateSet(capacityHint int) *StateSet {
	cap := 1
	// keep load factor <= ~0.6
	for cap < capacityHint*2 {
		cap <<= 1
	}
	if cap < 1024 {
		cap = 1024
	}
	return &StateSet{slots: make([]State, cap), mask: uint64(cap - 1)}
}

// hashState is the splitmix64 finalizer applied to the 64-bit board word.
func hashState(s State) uint64 {
	h := uint64(s)
	h ^= h >> 30
	h *= 0xBF58476D1CE4E5B9
	h ^= h >> 27
	h *= 0x94D049BB133111EB
	h ^= h >> 31
	return h
}

func (m *StateSet) grow() {
	old := m.slots
	m.slots = make([]State, len(old)*2)
	m.mask = uint64(len(m.slots) - 1)
	m.count = 0
	for _, s := range old {
		if s != 0 {
			m.add(s)
		}
	}
}

// add inserts s; returns true if it was newly added.
func (m *StateSet) add(s State) bool {
	if (m.count+1)*5 >= len(m.slots)*3 { // load factor 0.6
		m.grow()
	}
	i := hashState(s) & m.mask
	for {
		c := m.slots[i]
		if c == 0 {
			m.slots[i] = s
			m.count++
			return true
		}
		if c == s {
			return false
		}
		i = (i + 1) & m.mask
	}
}

func (m *StateSet) has(s State) bool {
	i := hashState(s) & m.mask
	for {
		c := m.slots[i]
		if c == 0 {
			return false
		}
		if c == s {
			return true
		}
		i = (i + 1) & m.mask
	}
}

func (m *StateSet) len() int { return m.count }

// clear empties the set while keeping its allocated capacity, so it can be
// reused across passes without reallocating.
func (m *StateSet) clear() {
	for i := range m.slots {
		m.slots[i] = 0
	}
	m.count = 0
}
