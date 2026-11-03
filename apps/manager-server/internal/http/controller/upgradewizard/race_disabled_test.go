//go:build !race

package upgradewizard

// raceDetectorEnabled scales the wall-clock budgets in this package's tests; it
// is false in a normal run, where the budgets are the behaviour's own.
const raceDetectorEnabled = false
