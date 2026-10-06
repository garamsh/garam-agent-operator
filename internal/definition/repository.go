package definition

import "context"

// Repository persists the control service's desired state.
// Every method that returns a value returns a copy the caller may change freely.
type Repository interface {
	// PublishProfile stores settings as the next version of org's named profile.
	PublishProfile(ctx context.Context, org, name string, settings ExecutionSettings) (Profile, error)
	// PublishProfileVersion stores p as the version it names of org's p.Name, and reports whether
	// it stored it, deciding in one step: under a version already published it stores nothing and
	// returns the stored profile where its settings are Same as p's, and ErrProfileVersionConflict
	// otherwise; a version neither published nor one past the latest is ErrProfileVersionGap.
	PublishProfileVersion(ctx context.Context, org string, p Profile) (Profile, bool, error)
	// GetProfile returns ErrNotFound for a version org never published.
	GetProfile(ctx context.Context, org string, ref ProfileRef) (Profile, error)

	// PublishTemplate stores the next version of org's t.Name; t.Version is ignored. t.Profile is
	// one of org's profiles.
	PublishTemplate(ctx context.Context, org string, t Template) (Template, error)
	// GetTemplate returns ErrNotFound for a version org never published.
	GetTemplate(ctx context.Context, org string, ref TemplateRef) (Template, error)
	// ListTemplates returns the latest version of each of org's templates, ordered by name.
	ListTemplates(ctx context.Context, org string) ([]Template, error)
	// ListProfiles returns every version org published of every profile, ordered by name and
	// then version.
	ListProfiles(ctx context.Context, org string) ([]ProfileRef, error)
	// PublishOnce stores t as the next version of org's t.Name and records p under p.Key with that
	// version, unless p.Key is already recorded; it returns the publication recorded under the key
	// either way, and whether this call stored it. t.Profile is one of org's profiles.
	PublishOnce(ctx context.Context, p Publication, t Template) (Publication, bool, error)

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
	// GetStatus returns the agent's stored status, the zero Status before any report.
	GetStatus(ctx context.Context, agent GRN) (Status, error)
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
	// CreationOf returns the registered creation of agent, or ErrNotFound.
	CreationOf(ctx context.Context, agent GRN) (Creation, error)
	// ArchivedRegistration reports whether a creation registered under an earlier release and
	// archived by migration 2 names agent (ADR 0058). It is the archive's one reader.
	ArchivedRegistration(ctx context.Context, agent GRN) (bool, error)

	// BeginInitialCertificate stores r as agent's pending first-certificate request unless one is
	// already stored, and returns the one stored either way.
	BeginInitialCertificate(ctx context.Context, agent GRN, r CertificateRequest) (InitialCertificate, error)
	// IssueInitialCertificate records issued as the result of agent's stored request when it is
	// still pending and equal to r. It reports whether this call recorded it, and returns what is
	// stored; ErrNotFound when no request is stored.
	IssueInitialCertificate(
		ctx context.Context, agent GRN, r CertificateRequest, issued IssuedCertificate,
	) (InitialCertificate, bool, error)
	// RegisterPlacement decides in with DecidePlacement against the agent's current placement and
	// the stored placement of the same Pod, and applies the decision in the same step: a new
	// placement supersedes the current one, revoking its token digest, before it is stored. It
	// returns the placement now current for the Pod, and reports whether this call stored it.
	RegisterPlacement(ctx context.Context, in PlacementInput) (Placement, bool, error)

	// BeginCutoverImport stores imp and the inactive revision 1 its import makes, d, together,
	// unless an import is stored for the agent; it returns the import stored either way, and
	// reports whether this call stored it. An agent with a revision and no import is
	// ErrAlreadyDefined.
	BeginCutoverImport(ctx context.Context, imp CutoverImport, d Definition) (CutoverImport, bool, error)
	// GetCutoverImport returns the agent's cutover import, or ErrNotFound.
	GetCutoverImport(ctx context.Context, agent GRN) (CutoverImport, error)
	// FreezeCutoverImport records the import frozen where it is imported or frozen, else
	// ErrCutoverStage; another import id is ErrImportOpen.
	FreezeCutoverImport(ctx context.Context, agent GRN, importID string) error
	// SwitchCutoverImport records the frozen import switched under configureRef, and in the same
	// step makes its revision 1 active, recorded for the import's assignee and epoch at a new
	// position. A switched one is answered as it is; an imported one is ErrCutoverStage.
	SwitchCutoverImport(ctx context.Context, agent GRN, importID, configureRef string) error
	// DiscardCutoverImport removes an import that is not switched, with its revision 1. A
	// switched one is ErrReverseMigrationRequired.
	DiscardCutoverImport(ctx context.Context, agent GRN, importID string) error

	// CurrentPlacement returns the agent's current placement, or ErrNotFound.
	CurrentPlacement(ctx context.Context, agent GRN) (Placement, error)

	// WithAgentLock runs fn while holding the agent's activation lock, which serializes every
	// activation attempt for the agent across every instance of the service sharing the store.
	WithAgentLock(ctx context.Context, agent GRN, fn func(context.Context) error) error
	// InsertActivation stores a unless an activation request is stored under its key, and
	// returns the one stored either way.
	InsertActivation(ctx context.Context, a Activation) (Activation, error)
	// GetActivation returns the activation request stored under the key, or ErrNotFound.
	GetActivation(ctx context.Context, agent GRN, requestID string) (Activation, error)
	// RecordActivation records the activation garam answered for the stored request, and makes it
	// the agent's latest. A request already answered with another is ErrActivationMismatch.
	RecordActivation(ctx context.Context, agent GRN, requestID, activationID string) error
	// LatestActivation returns the agent's most recent activation, current or ended, or
	// ErrNotFound before the first.
	LatestActivation(ctx context.Context, agent GRN) (string, error)
	// ActivationOfGeneration returns the activation garam answered for the agent's generation, or
	// ErrNotFound.
	ActivationOfGeneration(ctx context.Context, agent GRN, generation string) (string, error)
	// ConfigureReference returns the operation reference of the configure request that applied
	// the agent's revision, or ErrNotFound.
	ConfigureReference(ctx context.Context, agent GRN, revision Revision) (string, error)
	// RecordRuntimeApplied records the revision the agent's runtime reported effective.
	RecordRuntimeApplied(ctx context.Context, agent GRN, applied RuntimeApplied) error
	// GetRuntimeApplied returns what RecordRuntimeApplied last recorded, or ErrNotFound.
	GetRuntimeApplied(ctx context.Context, agent GRN) (RuntimeApplied, error)

	// ClearInitialCertificate removes agent's stored request when it is still pending and equal
	// to r, deciding and removing it in one step. Anything else is left as it is.
	ClearInitialCertificate(ctx context.Context, agent GRN, r CertificateRequest) error

	// OpenRecovery stores r as requested unless a recovery is stored under its console key, and
	// returns the one stored under the key either way, and whether this call stored it. A new one
	// for an agent with another open is ErrRecoveryOpen. Storing one moves the position.
	OpenRecovery(ctx context.Context, r Recovery) (Recovery, bool, error)
	// LatestRecovery returns the agent's open recovery, else its most recent one, or ErrNotFound.
	LatestRecovery(ctx context.Context, agent GRN) (Recovery, error)
	// PrepareRecovery stores body as the request of the agent's recovery requestID, which must
	// be requested, or prepared with the same body; it returns the recovery stored. No such
	// recovery is ErrNotFound, another epoch than its own ErrRecoveryEpoch, another body
	// ErrRequestReused, and one already finalized with another body ErrRecoveryStage.
	PrepareRecovery(ctx context.Context, agent GRN, requestID, epoch string, body []byte) (Recovery, error)
	// FinalizeRecovery records c as the answer to the agent's prepared recovery requestID, and
	// returns the recovery stored. A finalized one is answered as it is; a requested one is
	// ErrRecoveryStage. Finalizing one moves the position.
	FinalizeRecovery(ctx context.Context, agent GRN, requestID string, c RecoveredCredential) (Recovery, error)

	// RecordStop stores s as the agent's current stop unless a stop is stored under its console
	// key, and returns the one stored under the key either way, and whether this call stored it.
	// A new one for an agent a stop already holds is ErrAgentStopped. Storing one moves the
	// position. s.ActivationID is the caller's; the store records it as given.
	RecordStop(ctx context.Context, s Stop) (Stop, bool, error)
	// RecordDeactivation records that garam answered the deactivation of the stop stored under key.
	RecordDeactivation(ctx context.Context, key RequestKey) error
	// RecordStart ends the agent's current stop under end, unless a stop is already ended under
	// end's key, which is returned as it is. With no current stop it is ErrAgentNotStopped.
	// Ending one moves the position.
	RecordStart(ctx context.Context, agent GRN, end StopEnd) (Stop, error)
	// CurrentStop returns the stop holding the agent, or ErrNotFound.
	CurrentStop(ctx context.Context, agent GRN) (Stop, error)
}
