package permit

import "github.com/wyw14/solo-036/internal/model"

func (e *Evaluator) AllowRepressurization(proof model.PassageEvidence, durable bool) bool {
	if !model.ValidEvidence(proof) || !durable {
		return false
	}
	e.open[proof.RunID] = true
	e.generation[proof.RunID] = proof.Generation
	return true
}

// AcceptsCommand is the side-effect-free predicate behind AllowReceive. It
// only validates the command receipt; it never opens the permit. The launch
// re-check and the read-only preflight share this exact decision.
func AcceptsCommand(runID string, proof model.CommandReceipt, expected int64) bool {
	if runID == "" || proof.RunID != runID || !proof.Accepted || proof.Epoch != expected {
		return false
	}
	return true
}
func (e *Evaluator) AllowReceive(runID string, proof model.CommandReceipt, expected int64) bool {
	if !AcceptsCommand(runID, proof, expected) {
		return false
	}
	e.open[runID] = true
	return true
}
func (e *Evaluator) IsOpen(runID string) bool { return e.open[runID] }
