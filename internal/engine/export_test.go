package engine

import "time"

// SetDeployHealthDelay shortens CheckDeployHealth's settle delay for tests.
func SetDeployHealthDelay(d time.Duration) (restore func()) {
	old := deployHealthDelay
	deployHealthDelay = d
	return func() { deployHealthDelay = old }
}
