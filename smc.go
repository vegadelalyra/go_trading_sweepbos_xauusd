package main

// SMC primitives (Analyze, DetectSwings/FVGs/Structure/OrderBlocks plus the
// supporting types) live in internal/smc. The aliases in aliases.go bind
// `package main`'s legacy bare-name references to that package. This file is
// intentionally empty and will be removed once main no longer holds code that
// depends on the bare names.
