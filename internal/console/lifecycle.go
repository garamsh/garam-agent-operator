package console

import (
	"context"
	"errors"
	"fmt"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// OperationRecover is the operation an authority binds to open an agent's credential recovery, and
// the handoff garam spends to finalize one (garam@59fe68d api/machine.yaml recoverAgentCredential).
const OperationRecover = "agent:recover"

// Lifecycle carries an agent's credential recovery and the deactivation of its activation to
// garam under execution-fence.v1 (garam@59fe68d api/machine.yaml recoverAgentCredential,
// deactivateAgent). It returns *LifecycleRefusal or ErrLifecycleUndecided for the answers that
// refuse or decide nothing; any other error is the call's own.
type Lifecycle interface {
	// Recover sends body, exactly as given, under the agent:recover handoff an administrator
	// minted over it, which garam resolves and spends. garam compares the body's digest with the
	// one the handoff binds.
	Recover(ctx context.Context, agent string, handoff Authority, body []byte) (definition.RecoveredCredential, error)
	// Deactivate ends the agent's activation, if it is still the current one. An activation that
	// already ended is answered the same.
	Deactivate(ctx context.Context, agent, activationID string) error
}

// LifecycleRefusal is garam's refusal of a recovery or a deactivation, answered as garam gave it.
type LifecycleRefusal struct {
	Status  int
	Kind    string
	Message string
}

func (r *LifecycleRefusal) Error() string {
	return fmt.Sprintf("garam refused (%d %s): %s", r.Status, r.Kind, r.Message)
}

// ErrLifecycleUndecided is returned when garam did not decide a recovery or a deactivation. A
// recovery's outcome is then unknown, and is retried under the same request id and handoff.
var ErrLifecycleUndecided = errors.New("garam left the recovery or the deactivation undecided")
