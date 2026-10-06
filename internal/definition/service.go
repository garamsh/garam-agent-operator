package definition

import "context"

// Service is what the control service offers over its desired state.
type Service interface {
	PublishProfile(ctx context.Context, org, name string, settings ExecutionSettings) (Profile, error)
	PublishProfileVersion(ctx context.Context, org string, p Profile) (published Profile, created bool, err error)
	PublishTemplate(ctx context.Context, org string, t Template) (Template, error)
	Publish(ctx context.Context, in PublishInput) (p Publication, created bool, err error)
	ListTemplates(ctx context.Context, org string) ([]Template, error)
	GetTemplate(ctx context.Context, org string, ref TemplateRef) (Template, error)
	ListProfiles(ctx context.Context, org string) ([]ProfileRef, error)
	GetProfile(ctx context.Context, org string, ref ProfileRef) (Profile, error)
	Execution(ctx context.Context, org string, agent GRN) (Execution, error)
	CreateAgent(ctx context.Context, in CreateInput) (created Creation, first bool, err error)
	Configure(ctx context.Context, in ConfigureInput) (Applied, error)
	GetDefinition(ctx context.Context, agent GRN) (Definition, error)
	Position(ctx context.Context) (Position, error)
	Desired(ctx context.Context, operator string, limit int) (DesiredPage, error)
	RecordStatus(ctx context.Context, agent GRN, observed, rendered Revision) (Status, error)
	RequestInitialCertificate(ctx context.Context, in InitialCertificateInput) (c InitialCertificate, first bool, err error)
	RegisterPlacement(ctx context.Context, in PlacementInput) (p Placement, first bool, err error)
	CurrentPlacement(ctx context.Context, agent GRN) (Placement, error)
	ImportCutover(ctx context.Context, imp CutoverImport) (stored CutoverImport, first bool, err error)
	CutoverImportOf(ctx context.Context, agent GRN) (CutoverImport, error)
	FreezeCutover(ctx context.Context, agent GRN, importID string) error
	SwitchCutover(ctx context.Context, agent GRN, importID, configureRef string) error
	RollBackCutover(ctx context.Context, agent GRN, importID string) error
	WithActivationLock(ctx context.Context, agent GRN, fn func(context.Context) error) error
	PrepareActivation(ctx context.Context, agent GRN, req ActivationRequest) (Activation, error)
	RecordActivation(ctx context.Context, agent GRN, requestID, activationID string) error
	ActivationOfGeneration(ctx context.Context, agent GRN, generation string) (string, error)
	RecordRuntimeStatus(ctx context.Context, agent GRN, report RuntimeReport) error
	OpenRecovery(ctx context.Context, in OpenRecoveryInput) (r Recovery, first bool, err error)
	RecoveryOf(ctx context.Context, org string, agent GRN) (Recovery, error)
	PrepareRecovery(ctx context.Context, in InitialCertificateInput) (Recovery, error)
	FinalizeRecovery(ctx context.Context, agent GRN, requestID string, c RecoveredCredential) (Recovery, error)
	Stop(ctx context.Context, in StopInput) (s Stop, first bool, err error)
	RecordDeactivation(ctx context.Context, key RequestKey) error
	Start(ctx context.Context, in StopInput) (Stop, error)
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
