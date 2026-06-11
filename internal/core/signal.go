package core

import "time"

// Signal is what a strategy emits when it detects a tradeable setup. The
// engine still adds spread, enforces MaxSLPoints and tracks the lifecycle; a
// strategy is only responsible for the "physics" of the setup (entry, SL, TP)
// and the human-readable Reason.
type Signal struct {
	Strategy string
	Side     Side
	Entry    float64
	SL       float64
	TP       float64
	Index    int
	Time     time.Time
	Reason   string
}
