package strategy

import "sync"

var (
	mu       sync.RWMutex
	registry []Strategy
)

// Register makes a strategy available to All / ByName. Conventionally called
// from a sub-package's init() so the engine only needs to blank-import the
// package to pick it up. Registration order is preserved (it determines the
// priority order for first-match strategies in the engine loop).
func Register(s Strategy) {
	mu.Lock()
	defer mu.Unlock()
	registry = append(registry, s)
}

// All returns a defensive copy of the registry. Callers iterate over the
// returned slice; mutations on it have no effect on future calls.
func All() []Strategy {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Strategy, len(registry))
	copy(out, registry)
	return out
}

// ByName returns the registered strategy whose Meta().Name matches.
// Lookups are O(N) — N is at most a few dozen, so an explicit map is overkill.
func ByName(name string) (Strategy, bool) {
	mu.RLock()
	defer mu.RUnlock()
	for _, s := range registry {
		if s.Meta().Name == name {
			return s, true
		}
	}
	return nil, false
}

// ByFamily returns every registered strategy belonging to the given family.
func ByFamily(f Family) []Strategy {
	mu.RLock()
	defer mu.RUnlock()
	var out []Strategy
	for _, s := range registry {
		if s.Meta().Family == f {
			out = append(out, s)
		}
	}
	return out
}
