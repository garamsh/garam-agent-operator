package definition

import "context"

// Repository persists the control service's desired state.
// Every method that returns a value returns a copy the caller may change freely.
type Repository interface {
	// PublishProfile stores settings as the next version of the named profile.
	PublishProfile(ctx context.Context, name string, settings ExecutionSettings) (Profile, error)
	// GetProfile returns ErrNotFound for a version never published.
	GetProfile(ctx context.Context, ref ProfileRef) (Profile, error)

	// PublishTemplate stores the next version of t.Name; t.Version is ignored.
	PublishTemplate(ctx context.Context, t Template) (Template, error)
	// GetTemplate returns ErrNotFound for a version never published.
	GetTemplate(ctx context.Context, ref TemplateRef) (Template, error)

	// Configure stores r unless its key is already stored, and returns the request stored
	// under the key either way. A new request stores d with it when d.Revision is one past
	// the agent's latest, as Applied, and is stored as Stale otherwise. An agent with no
	// revision stores nothing and returns ErrNotFound. r.Outcome is ignored.
	Configure(ctx context.Context, r Request, d Definition) (Request, error)
	// GetDefinition returns the agent's latest revision, or ErrNotFound.
	GetDefinition(ctx context.Context, agent GRN) (Definition, error)
	// Desired returns the latest revision of each agent that is recorded for operator and was
	// stored after after, in the order stored, and the position the answer accounts up to.
	Desired(ctx context.Context, operator string, after Position) (DesiredPage, error)
	// RecordStatus raises the agent's stored status to s field by field, never lowering one,
	// and returns the status stored.
	RecordStatus(ctx context.Context, agent GRN, s Status) (Status, error)

	// BeginCreation stores c as Pending unless its key is already stored, and returns
	// the creation stored under the key either way.
	BeginCreation(ctx context.Context, c Creation) (Creation, error)
	// RegisterCreation records the key's creation as Registered and stores d, the
	// agent's first revision, together: neither is stored without the other.
	RegisterCreation(ctx context.Context, key RequestKey, d Definition) (Creation, error)
	// FailCreation records the key's creation as Failed.
	FailCreation(ctx context.Context, key RequestKey, reason string) (Creation, error)
}
