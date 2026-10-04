package definition

import "context"

// Repository persists the control service's desired state.
// Every method that returns a value returns a copy the caller may change freely.
type Repository interface {
	// PublishProfile stores settings as the next version of org's named profile.
	PublishProfile(ctx context.Context, org, name string, settings ExecutionSettings) (Profile, error)
	// GetProfile returns ErrNotFound for a version org never published.
	GetProfile(ctx context.Context, org string, ref ProfileRef) (Profile, error)

	// PublishTemplate stores the next version of org's t.Name; t.Version is ignored. t.Profile is
	// one of org's profiles.
	PublishTemplate(ctx context.Context, org string, t Template) (Template, error)
	// GetTemplate returns ErrNotFound for a version org never published.
	GetTemplate(ctx context.Context, org string, ref TemplateRef) (Template, error)

	// Configure stores r unless its key is already stored, and returns the request stored
	// under the key either way. A new request stores d with it when d.Revision is one past
	// the agent's latest, as Applied, and is stored as Stale otherwise. An agent with no
	// revision in d.Organization stores nothing and returns ErrNotFound. r.Outcome is ignored.
	Configure(ctx context.Context, r Request, d Definition) (Request, error)
	// GetDefinition returns the agent's latest revision, or ErrNotFound.
	GetDefinition(ctx context.Context, agent GRN) (Definition, error)
	// Position returns the position the latest stored revision took, 0 before any.
	Position(ctx context.Context) (Position, error)
	// Desired returns the latest revision of each agent recorded for operator, at most limit of
	// them in the order stored, and the position they were read at.
	Desired(ctx context.Context, operator string, limit int) (DesiredPage, error)
	// RecordStatus raises the agent's stored status to s field by field, never lowering one,
	// and returns the status stored.
	RecordStatus(ctx context.Context, agent GRN, s Status) (Status, error)

	// BeginCreation stores c as Pending unless its key is already stored, and returns
	// the creation stored under the key either way.
	BeginCreation(ctx context.Context, c Creation) (Creation, error)
	// RegisterCreation records the key's creation as Registered, under d's agent and the epoch
	// of d's assignment, and stores d, the agent's first revision, together: neither is stored
	// without the other.
	// It reports whether this call registered it, and is false for one an earlier call did.
	RegisterCreation(ctx context.Context, key RequestKey, d Definition) (Creation, bool, error)
	// FailCreation records the key's creation as failed.
	FailCreation(ctx context.Context, key RequestKey, failed Failed) (Creation, error)
}
