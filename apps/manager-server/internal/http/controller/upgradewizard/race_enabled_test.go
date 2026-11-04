//go:build race

package upgradewizard

// raceDetectorEnabled scales the wall-clock budgets in this package's tests.
// The detector multiplies the cost of the per-poll credential verification that
// surrounds each bounded mount lookup, so an aggregate budget that holds in a
// normal run is a detector-speed assertion here, not a behaviour one.
const raceDetectorEnabled = true
