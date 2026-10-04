package definition

import "context"

// Service is what the control service offers over its desired state.
type Service interface {
	PublishProfile(ctx context.Context, name string, settings ExecutionSettings) (Profile, error)
	PublishTemplate(ctx context.Context, t Template) (Template, error)
	CreateAgent(ctx context.Context, key RequestKey, actor string, template TemplateRef) (Creation, error)
	Configure(ctx context.Context, in ConfigureInput) (Applied, error)
	GetDefinition(ctx context.Context, agent GRN) (Definition, error)
	Position(ctx context.Context) (Position, error)
	Desired(ctx context.Context, operator string, limit int) (DesiredPage, error)
	RecordStatus(ctx context.Context, agent GRN, observed, rendered Revision) (Status, error)
}

type service struct {
	repository Repository
	registrar  Registrar
}

// NewService returns a Service storing in repository and registering agents through registrar.
func NewService(repository Repository, registrar Registrar) Service {
	return &service{repository: repository, registrar: registrar}
}
