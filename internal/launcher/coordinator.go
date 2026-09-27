package launcher

import (
	"fmt"

	"github.com/wyw14/solo-036/internal/model"
)

// closureVerdict is the outcome of exercising the closure/interlock sequence.
type closureVerdict int

const (
	ClosureRejected    closureVerdict = iota // ConfirmClosure proof was rejected
	ClosureInterlocked                       // proof accepted but CanLaunch interlocks blocked
	ClosureReady                             // interlocks would allow launch
)

// ClosureProbe reports what the closure sequence would decide without leaving
// any field state behind: the current receipt is restored and the pressure
// authorization granted by confirmation is rolled back.
type ClosureProbe struct {
	Verdict closureVerdict
	Receipt model.CommandReceipt
}

func (c *Controller) BeginClosure(runID string, epoch int64) {
	c.mu.Lock()
	c.current = model.CommandReceipt{RunID: runID, Command: "closure", Epoch: epoch, At: timeNow()}
	c.mu.Unlock()
}

func (c *Controller) ConfirmClosure(receipt model.CommandReceipt) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.confirmClosureLocked(receipt)
}

// confirmClosureLocked is the single closure-proof gate shared by the real
// chain and the read-only probe. Caller must hold c.mu.
func (c *Controller) confirmClosureLocked(receipt model.CommandReceipt) error {
	if receipt.RunID != c.current.RunID || receipt.Epoch != c.current.Epoch || !receipt.Accepted {
		return fmt.Errorf("closure proof rejected")
	}
	c.pressure.Authorize()
	return nil
}

// closureReceiptLocked mirrors the proof the real chain builds for runID at
// epoch, so the probe exercises exactly the same predicate. Caller holds c.mu.
func closureReceiptLocked(runID string, epoch int64) model.CommandReceipt {
	return model.CommandReceipt{RunID: runID, Command: "closure", Epoch: epoch, Accepted: true, At: timeNow()}
}

func (c *Controller) CanLaunch(receipt model.CommandReceipt) bool {
	return c.permits.AllowReceive(receipt.RunID, receipt, receipt.Epoch) && c.pressure.CanPressurize(receipt)
}

// evaluateClosureLocked runs the post-closure interlock gate and restores the
// permit mark that AllowReceive writes. Caller must hold c.mu; it must not hold
// the permit lock.
func (c *Controller) evaluateClosureLocked(receipt model.CommandReceipt) closureVerdict {
	probe := c.permits.GrantProbe(receipt.RunID)
	allowed := c.CanLaunch(receipt)
	c.permits.RestoreGrant(probe)
	if !allowed {
		return ClosureInterlocked
	}
	return ClosureReady
}

// ProbeClosure exercises BeginClosure -> ConfirmClosure -> CanLaunch for a
// hypothetical run and then restores every touched field (current receipt,
// pressure authorization, permit mark). It never reserves resources and never
// appends events, so operators can see whether the closure/interlock gate
// would block before an operation is created.
func (c *Controller) ProbeClosure(runID string, epoch int64) ClosureProbe {
	c.mu.Lock()
	defer c.mu.Unlock()

	previous := c.current
	pressureState := c.pressure.AuthorizationSnapshot()
	c.current = model.CommandReceipt{RunID: runID, Command: "closure", Epoch: epoch, At: timeNow()}
	receipt := closureReceiptLocked(runID, epoch)

	verdict := ClosureRejected
	if err := c.confirmClosureLocked(receipt); err == nil {
		verdict = c.evaluateClosureLocked(receipt)
	}

	c.current = previous
	c.pressure.AuthorizationRestore(pressureState)
	return ClosureProbe{Verdict: verdict, Receipt: receipt}
}

func (c *Controller) Current() model.CommandReceipt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}
