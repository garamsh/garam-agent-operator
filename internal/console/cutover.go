package console

import (
	"context"
	"errors"
	"fmt"
)

// Stage is one of a legacy agent's cutover routes on garam (garam@1a5273d, ADR-0086).
type Stage string

const (
	// StageRead is garam's read of the source, which the import stage carries.
	StageRead Stage = "read"
	// StageFreeze is garam's freeze, verified against the import's digest.
	StageFreeze Stage = "freeze"
	// StageSwitch is garam's record that control owns the agent's source.
	StageSwitch Stage = "switch"
	// StageRollback is garam's end of a frozen attempt.
	StageRollback Stage = "rollback"
)

// CutoverSource is garam's answer to the read: the agent's assignment and mode, its source, and
// the digest of that source. HasSource is false for an agent no definition describes, whose values
// are empty and whose digest names the assignee as its operator.
type CutoverSource struct {
	Agent        string
	Assignee     string
	Epoch        string
	Mode         string
	Class        string
	HasSource    bool
	Operator     string
	Values       map[string]string
	SourceDigest string
}

// CutoverAttempt is an attempt as garam recorded it.
type CutoverAttempt struct {
	ImportID     string
	Stage        string
	FrozenDigest string
}

// CutoverRefusal is garam's refusal of a cutover stage, answered as garam gave it: the status, and
// as kind the contract's reason where garam named one.
type CutoverRefusal struct {
	Status  int
	Kind    string
	Message string
}

func (r *CutoverRefusal) Error() string {
	return fmt.Sprintf("garam refused the cutover stage (%d %s): %s", r.Status, r.Kind, r.Message)
}

// ErrCutoverUndecided is returned when garam could not decide a cutover stage from current state.
var ErrCutoverUndecided = errors.New("garam left the cutover stage undecided")

// Cutover carries a legacy agent's cutover stages to garam under agent-cutover.v1, each under the
// durable agent:cutover reference ref minted for that stage's route. It returns *CutoverRefusal or
// ErrCutoverUndecided for the answers that refuse; any other error is the call's own.
type Cutover interface {
	// Target is the exact request target of the stage's route, as the reference for it must bind.
	Target(agent string, stage Stage) string
	Read(ctx context.Context, agent, ref string) (CutoverSource, error)
	Freeze(ctx context.Context, agent, ref, importID, sourceDigest, epoch string) (CutoverAttempt, error)
	Switch(ctx context.Context, agent, ref, importID string) (CutoverAttempt, error)
	RollBack(ctx context.Context, agent, ref, importID string) (CutoverAttempt, error)
}
