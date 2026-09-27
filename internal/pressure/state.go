package pressure

// AuthorizationState is a point-in-time view of the pressure controller used to
// run read-only interlock probes and then restore the controller exactly.
type AuthorizationState struct {
	Current float64
	Enabled bool
	Stopped bool
}

func (c *Controller) Current() float64 { c.mu.Lock(); defer c.mu.Unlock(); return c.current }
func (c *Controller) History() []float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]float64(nil), c.history...)
}

// AuthorizationSnapshot captures the authorization state without changing it.
func (c *Controller) AuthorizationSnapshot() AuthorizationState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return AuthorizationState{Current: c.current, Enabled: c.enabled, Stopped: c.stopped}
}

// AuthorizationRestore reapplies a previously captured state, undoing a probe.
func (c *Controller) AuthorizationRestore(state AuthorizationState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = state.Current
	c.enabled = state.Enabled
	c.stopped = state.Stopped
}
