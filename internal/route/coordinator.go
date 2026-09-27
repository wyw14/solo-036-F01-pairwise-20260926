package route

import (
	"fmt"
	"github.com/wyw14/solo-036/internal/model"
	"time"
)

// OwnerConflict is the rejection reason Reserve returns and preflight reports
// when the route is fenced to another run. It is shared so the read-only
// occupancy verdict matches the real launch decision word for word.
func OwnerConflict(owner model.RouteLease) error {
	return fmt.Errorf("route owned by %s", owner.RunID)
}

// Owner returns the current lease holder without changing reservation state.
// A prospective launch is never the existing owner, so any returned lease is a
// blocking occupancy; Reserve applies the identical ownership predicate.
func (c *Coordinator) Owner(route string) (model.RouteLease, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lease, ok := c.owners[route]
	return lease, ok
}

func (c *Coordinator) Reserve(route, runID string, epoch int64) (model.RouteLease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if owner, ok := c.owners[route]; ok && owner.RunID != runID {
		return model.RouteLease{}, OwnerConflict(owner)
	}
	lease := model.RouteLease{Route: route, RunID: runID, Epoch: epoch, AcquiredAt: time.Now().UTC()}
	c.owners[route] = lease
	return lease, nil
}
func (c *Coordinator) Release(route, runID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if owner, ok := c.owners[route]; ok && owner.RunID == runID {
		delete(c.owners, route)
		delete(c.progress, runID)
	}
}
