package definition

import "context"

// Service is what the control service offers over its desired state.
type Service interface {
	PublishProfile(ctx context.Context, org, name string, settings ExecutionSettings) (Profile, error)
	PublishTemplate(ctx context.Context, org string, t Template) (Template, error)
	CreateAgent(ctx context.Context, in CreateInput) (created Creation, first bool, err error)
	Configure(ctx context.Context, in ConfigureInput) (Applied, error)
	GetDefinition(ctx context.Context, agent GRN) (Definition, error)
	Position(ctx context.Context) (Position, error)
	Desired(ctx context.Context, operator string, limit int) (DesiredPage, error)
	RecordStatus(ctx context.Context, agent GRN, observed, rendered Revision) (Status, error)
	RequestInitialCertificate(ctx context.Context, in InitialCertificateInput) (c InitialCertificate, first bool, err error)
	RegisterPlacement(ctx context.Context, in PlacementInput) (p Placement, first bool, err error)
}

type service struct {
	repository Repository
	registrar  Registrar
	issuer     Issuer
}

// NewService returns a Service storing in repository, registering agents through registrar and
// asking for their first certificates through issuer.
func NewService(repository Repository, registrar Registrar, issuer Issuer) Service {
	return &service{repository: repository, registrar: registrar, issuer: issuer}
}
