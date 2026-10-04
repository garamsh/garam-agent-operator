package definition

import "context"

// Service is what the control service offers over its desired state.
type Service interface {
	PublishProfile(ctx context.Context, name string, settings ExecutionSettings) (Profile, error)
	PublishTemplate(ctx context.Context, t Template) (Template, error)
	CreateAgent(ctx context.Context, key CreationKey, template TemplateRef) (Creation, error)
	UpdateDefinition(ctx context.Context, in UpdateInput) (Definition, error)
	GetDefinition(ctx context.Context, agent GRN) (Definition, error)
}

// UpdateInput is a change to an agent's definition, stating the revision it was based on.
type UpdateInput struct {
	Agent   GRN
	BasedOn Revision
	Profile ProfileRef
	Config  Configuration
}

type service struct {
	repository Repository
	registrar  Registrar
}

// NewService returns a Service storing in repository and registering agents through registrar.
func NewService(repository Repository, registrar Registrar) Service {
	return &service{repository: repository, registrar: registrar}
}
