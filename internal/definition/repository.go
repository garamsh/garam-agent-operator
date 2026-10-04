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

	// AppendDefinition stores d only when d.Revision is one past the agent's latest,
	// and returns ErrStaleRevision otherwise, or ErrNotFound for an agent with none.
	AppendDefinition(ctx context.Context, d Definition) error
	// GetDefinition returns the agent's latest revision, or ErrNotFound.
	GetDefinition(ctx context.Context, agent GRN) (Definition, error)

	// BeginCreation stores c as Pending unless its key is already stored, and returns
	// the creation stored under the key either way.
	BeginCreation(ctx context.Context, c Creation) (Creation, error)
	// RegisterCreation records the key's creation as Registered and stores d, the
	// agent's first revision, together: neither is stored without the other.
	RegisterCreation(ctx context.Context, key CreationKey, d Definition) (Creation, error)
	// FailCreation records the key's creation as Failed.
	FailCreation(ctx context.Context, key CreationKey, reason string) (Creation, error)
}
