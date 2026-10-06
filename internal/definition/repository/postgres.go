package repository

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

//go:embed schema.sql
var schema string

// uniqueViolation is PostgreSQL's SQLSTATE for a duplicate key.
const uniqueViolation = "23505"

// Postgres is a definition.Repository in the control service's PostgreSQL database.
type Postgres struct {
	pool *pgxpool.Pool
}

var _ definition.Repository = (*Postgres)(nil)

// NewPostgres returns a Postgres storing through pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// ApplySchema creates whatever part of the schema the database does not hold yet.
func (p *Postgres) ApplySchema(ctx context.Context) error {
	if _, err := p.pool.Exec(ctx, schema); err != nil {
		return storeError("apply schema", err)
	}
	return nil
}

// settingsColumn is a profile's settings as stored.
type settingsColumn struct {
	Resources            corev1.ResourceRequirements `json:"resources"`
	StorageSize          resource.Quantity           `json:"storageSize"`
	StorageClassName     *string                     `json:"storageClassName,omitempty"`
	WorkspaceStorageSize *resource.Quantity          `json:"workspaceStorageSize,omitempty"`
}

// configColumn is a configuration as stored.
type configColumn struct {
	Provider  string            `json:"provider"`
	BaseURL   string            `json:"baseURL"`
	ModelName string            `json:"modelName"`
	APIKeyRef string            `json:"apiKeyRef"`
	Ego       string            `json:"ego"`
	Tools     map[string]string `json:"tools"`

	Embedding *embeddingColumn `json:"embedding,omitempty"`
}

// embeddingColumn is a model's embeddings endpoint as stored, absent where it names none.
type embeddingColumn struct {
	BaseURL   string `json:"baseURL"`
	Name      string `json:"name"`
	APIKeyRef string `json:"apiKeyRef"`
}

func encodeConfig(c definition.Configuration) ([]byte, error) {
	column := configColumn{
		Provider:  c.Model.Provider,
		BaseURL:   c.Model.BaseURL,
		ModelName: c.Model.Name,
		APIKeyRef: string(c.Model.APIKey),
		Ego:       c.Ego,
		Tools:     c.Tools,
	}
	if e := c.Model.Embedding; e != nil {
		column.Embedding = &embeddingColumn{BaseURL: e.BaseURL, Name: e.Name, APIKeyRef: string(e.APIKey)}
	}
	return json.Marshal(column)
}

func decodeConfig(raw []byte) (definition.Configuration, error) {
	var c configColumn
	if err := json.Unmarshal(raw, &c); err != nil {
		return definition.Configuration{}, storeError("decode configuration", err)
	}
	config := definition.Configuration{
		Model: definition.Model{
			Provider: c.Provider,
			BaseURL:  c.BaseURL,
			Name:     c.ModelName,
			APIKey:   definition.SecretRef(c.APIKeyRef),
		},
		Ego:   c.Ego,
		Tools: c.Tools,
	}
	if e := c.Embedding; e != nil {
		config.Model.Embedding = &definition.Embedding{
			BaseURL: e.BaseURL, Name: e.Name, APIKey: definition.SecretRef(e.APIKeyRef),
		}
	}
	return config, nil
}

func (p *Postgres) PublishProfile(ctx context.Context, org, name string, settings definition.ExecutionSettings) (definition.Profile, error) {
	raw, err := json.Marshal(settingsColumn(settings))
	if err != nil {
		return definition.Profile{}, storeError("encode settings", err)
	}
	var version int64
	err = p.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockProfiles); err != nil {
			return err
		}
		return tx.QueryRow(ctx, publishProfile, org, name, raw).Scan(&version)
	})
	if err != nil {
		return definition.Profile{}, storeError("publish profile", err)
	}
	return p.GetProfile(ctx, org, definition.ProfileRef{Name: name, Version: definition.Version(version)})
}

func (p *Postgres) GetProfile(ctx context.Context, org string, ref definition.ProfileRef) (definition.Profile, error) {
	var raw []byte
	if err := p.pool.QueryRow(ctx, getProfile, org, ref.Name, int64(ref.Version)).Scan(&raw); err != nil {
		return definition.Profile{}, notFound("get profile", err)
	}
	var s settingsColumn
	if err := json.Unmarshal(raw, &s); err != nil {
		return definition.Profile{}, storeError("decode settings", err)
	}
	return definition.Profile{Name: ref.Name, Version: ref.Version, Settings: definition.ExecutionSettings(s)}, nil
}

func (p *Postgres) PublishTemplate(ctx context.Context, org string, t definition.Template) (definition.Template, error) {
	raw, err := encodeConfig(t.Config)
	if err != nil {
		return definition.Template{}, storeError("encode configuration", err)
	}
	var version int64
	err = p.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockTemplates); err != nil {
			return err
		}
		return tx.QueryRow(ctx, publishTemplate, org, t.Name, t.Profile.Name, int64(t.Profile.Version), raw).Scan(&version)
	})
	if err != nil {
		return definition.Template{}, storeError("publish template", err)
	}
	return p.GetTemplate(ctx, org, definition.TemplateRef{Name: t.Name, Version: definition.Version(version)})
}

func (p *Postgres) GetTemplate(ctx context.Context, org string, ref definition.TemplateRef) (definition.Template, error) {
	var (
		profileName    string
		profileVersion int64
		raw            []byte
	)
	err := p.pool.QueryRow(ctx, getTemplate, org, ref.Name, int64(ref.Version)).Scan(&profileName, &profileVersion, &raw)
	if err != nil {
		return definition.Template{}, notFound("get template", err)
	}
	config, err := decodeConfig(raw)
	if err != nil {
		return definition.Template{}, err
	}
	return definition.Template{
		Name:    ref.Name,
		Version: ref.Version,
		Profile: definition.ProfileRef{Name: profileName, Version: definition.Version(profileVersion)},
		Config:  config,
	}, nil
}

func (p *Postgres) ListTemplates(ctx context.Context, org string) ([]definition.Template, error) {
	rows, err := p.pool.Query(ctx, listTemplates, org)
	if err != nil {
		return nil, storeError("list templates", err)
	}
	defer rows.Close()
	var latest []definition.Template
	for rows.Next() {
		var (
			t                       definition.Template
			version, profileVersion int64
			raw                     []byte
		)
		if err := rows.Scan(&t.Name, &version, &t.Profile.Name, &profileVersion, &raw); err != nil {
			return nil, storeError("list templates", err)
		}
		t.Version, t.Profile.Version = definition.Version(version), definition.Version(profileVersion)
		if t.Config, err = decodeConfig(raw); err != nil {
			return nil, err
		}
		latest = append(latest, t)
	}
	if err := rows.Err(); err != nil {
		return nil, storeError("list templates", err)
	}
	return latest, nil
}

func (p *Postgres) ListProfiles(ctx context.Context, org string) ([]definition.ProfileRef, error) {
	rows, err := p.pool.Query(ctx, listProfiles, org)
	if err != nil {
		return nil, storeError("list profiles", err)
	}
	defer rows.Close()
	var refs []definition.ProfileRef
	for rows.Next() {
		var (
			ref     definition.ProfileRef
			version int64
		)
		if err := rows.Scan(&ref.Name, &version); err != nil {
			return nil, storeError("list profiles", err)
		}
		ref.Version = definition.Version(version)
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, storeError("list profiles", err)
	}
	return refs, nil
}

// PublishOnce publishes and records the publication in one transaction, under the templates
// lock, so a concurrent repeat of the key waits and then reads the first's version.
func (p *Postgres) PublishOnce(ctx context.Context, pub definition.Publication, t definition.Template) (
	definition.Publication, bool, error,
) {
	raw, err := encodeConfig(t.Config)
	if err != nil {
		return definition.Publication{}, false, storeError("encode configuration", err)
	}
	key, b := pub.Key, pub.Binding
	var (
		stored  definition.Publication
		created bool
	)
	err = p.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockTemplates); err != nil {
			return err
		}
		var version int64
		stored = definition.Publication{Key: key}
		sb := &stored.Binding
		err := tx.QueryRow(ctx, getPublication, key.Organization, key.RequestID).
			Scan(&sb.Actor, &sb.Operation, &sb.Target, &sb.BodySHA256, &sb.OperationRef, &stored.Template.Name, &version)
		if err == nil {
			stored.Template.Version = definition.Version(version)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, publishTemplate, key.Organization, t.Name, t.Profile.Name,
			int64(t.Profile.Version), raw).Scan(&version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, insertPublication, key.Organization, key.RequestID, b.Actor, b.Operation, b.Target,
			b.BodySHA256, b.OperationRef, t.Name, version); err != nil {
			return err
		}
		stored = definition.Publication{Key: key, Binding: b,
			Template: definition.TemplateRef{Name: t.Name, Version: definition.Version(version)}}
		created = true
		return nil
	})
	if err != nil {
		return definition.Publication{}, false, storeError("publish", err)
	}
	return stored, created, nil
}

func (p *Postgres) GetStatus(ctx context.Context, agent definition.GRN) (definition.Status, error) {
	var (
		observed, rendered int64
		applied            *int64
	)
	err := p.pool.QueryRow(ctx, getStatus, string(agent)).Scan(&observed, &rendered, &applied)
	if errors.Is(err, pgx.ErrNoRows) {
		return definition.Status{}, nil
	}
	if err != nil {
		return definition.Status{}, storeError("get status", err)
	}
	s := definition.Status{Observed: definition.Revision(observed), Rendered: definition.Revision(rendered)}
	if applied != nil {
		r := definition.Revision(*applied)
		s.Applied = &r
	}
	return s, nil
}

func (p *Postgres) Configure(ctx context.Context, r definition.Request, d definition.Definition) (definition.Request, error) {
	raw, err := encodeConfig(d.Config)
	if err != nil {
		return definition.Request{}, storeError("encode configuration", err)
	}
	b := r.Binding
	var stored definition.Request
	err = p.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, beginRequest, r.Key.Organization, r.Key.RequestID, b.Actor, b.Operation, b.Target,
			b.BodySHA256, b.OperationRef, b.Assignment.Operator, b.Assignment.Epoch, string(r.Agent))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			stored, err = scanRequest(tx.QueryRow(ctx, getRequest, r.Key.Organization, r.Key.RequestID), r.Key)
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, definitionExists, string(d.Agent), d.Organization).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return definition.ErrNotFound
		}
		stored = r
		stored.Outcome = definition.Stale{}
		applied, err := appendDefinitionIn(ctx, tx, d, raw)
		if err != nil || !applied {
			return err
		}
		if _, err := tx.Exec(ctx, applyRequest, r.Key.Organization, r.Key.RequestID, int64(d.Revision)); err != nil {
			return err
		}
		stored.Outcome = definition.Applied{Revision: d.Revision}
		return nil
	})
	if err != nil {
		return definition.Request{}, storeError("configure", err)
	}
	return stored, nil
}

// appendDefinitionIn stores d in tx when its revision is one past the agent's latest. It runs
// under a savepoint, so a revision another transaction stored first leaves tx usable.
func appendDefinitionIn(ctx context.Context, tx pgx.Tx, d definition.Definition, raw []byte) (bool, error) {
	var applied bool
	err := pgx.BeginFunc(ctx, tx, func(savepoint pgx.Tx) error {
		operator, epoch := assignmentColumns(d.Assignment)
		tag, err := savepoint.Exec(ctx, appendDefinition,
			string(d.Agent), d.Organization, int64(d.Revision), d.Profile.Name, int64(d.Profile.Version), raw, operator, epoch)
		applied = err == nil && tag.RowsAffected() == 1
		return err
	})
	if isUniqueViolation(err) {
		return false, nil
	}
	return applied, err
}

// assignmentColumns is a recorded assignment as its two nullable columns.
func assignmentColumns(a *definition.Assignment) (operator, epoch *string) {
	if a == nil {
		return nil, nil
	}
	return &a.Operator, &a.Epoch
}

// assignmentOf is the assignment two nullable columns record, or nil.
func assignmentOf(operator, epoch *string) *definition.Assignment {
	if operator == nil || epoch == nil {
		return nil
	}
	return &definition.Assignment{Operator: *operator, Epoch: *epoch}
}

func (p *Postgres) Position(ctx context.Context) (definition.Position, error) {
	var position int64
	if err := p.pool.QueryRow(ctx, getPosition).Scan(&position); err != nil {
		return 0, storeError("position", err)
	}
	return definition.Position(position), nil
}

func (p *Postgres) Desired(ctx context.Context, operator string, limit int) (definition.DesiredPage, error) {
	var page definition.DesiredPage
	// One snapshot for both reads: the position read accounts for exactly the revisions read.
	err := pgx.BeginTxFunc(ctx, p.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			var position int64
			if err := tx.QueryRow(ctx, getPosition).Scan(&position); err != nil {
				return err
			}
			page.Position = definition.Position(position)
			rows, err := tx.Query(ctx, desired, operator, limit)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				r, err := scanDesired(rows, operator)
				if err != nil {
					return err
				}
				page.Revisions = append(page.Revisions, r)
			}
			return rows.Err()
		})
	if err != nil {
		return definition.DesiredPage{}, storeError("desired", err)
	}
	return page, nil
}

func scanDesired(row pgx.Row, operator string) (definition.DesiredRevision, error) {
	var (
		agent, organization   string
		revision, profileVers int64
		profileName           string
		config, settings      []byte
		epoch                 string
		cutover               bool
	)
	if err := row.Scan(&agent, &organization, &revision, &profileName, &profileVers, &config, &epoch, &settings,
		&cutover); err != nil {
		return definition.DesiredRevision{}, err
	}
	c, err := decodeConfig(config)
	if err != nil {
		return definition.DesiredRevision{}, err
	}
	var s settingsColumn
	if err := json.Unmarshal(settings, &s); err != nil {
		return definition.DesiredRevision{}, fmt.Errorf("decode settings: %v", err)
	}
	return definition.DesiredRevision{
		Definition: definition.Definition{
			Agent:        definition.GRN(agent),
			Organization: organization,
			Revision:     definition.Revision(revision),
			Profile:      definition.ProfileRef{Name: profileName, Version: definition.Version(profileVers)},
			Config:       c,
			Assignment:   &definition.Assignment{Operator: operator, Epoch: epoch},
		},
		Settings: definition.ExecutionSettings(s),
		Cutover:  cutover,
	}, nil
}

func (p *Postgres) RecordStatus(ctx context.Context, agent definition.GRN, s definition.Status) (definition.Status, error) {
	var (
		observed, rendered int64
		applied            *int64
	)
	err := p.pool.QueryRow(ctx, recordStatus, string(agent), int64(s.Observed), int64(s.Rendered)).
		Scan(&observed, &rendered, &applied)
	if err != nil {
		return definition.Status{}, storeError("record status", err)
	}
	stored := definition.Status{Observed: definition.Revision(observed), Rendered: definition.Revision(rendered)}
	if applied != nil {
		r := definition.Revision(*applied)
		stored.Applied = &r
	}
	return stored, nil
}

// scanRequest reads one configure request row, the outcome from its outcome column.
func scanRequest(row pgx.Row, key definition.RequestKey) (definition.Request, error) {
	var (
		r        definition.Request
		agent    string
		outcome  string
		revision *int64
	)
	b := &r.Binding
	err := row.Scan(&b.Actor, &b.Operation, &b.Target, &b.BodySHA256, &b.OperationRef,
		&b.Assignment.Operator, &b.Assignment.Epoch, &agent, &outcome, &revision)
	if err != nil {
		return definition.Request{}, notFound("get request", err)
	}
	r.Key = key
	r.Agent = definition.GRN(agent)
	switch {
	case outcome == "applied" && revision != nil:
		r.Outcome = definition.Applied{Revision: definition.Revision(*revision)}
	case outcome == "stale":
		r.Outcome = definition.Stale{}
	default:
		return definition.Request{}, fmt.Errorf("request in outcome %q violates the schema's checks", outcome)
	}
	return r, nil
}

func (p *Postgres) GetDefinition(ctx context.Context, agent definition.GRN) (definition.Definition, error) {
	var (
		organization    string
		revision        int64
		profileName     string
		profileVersion  int64
		raw             []byte
		operator, epoch *string
	)
	err := p.pool.QueryRow(ctx, getDefinition, string(agent)).
		Scan(&organization, &revision, &profileName, &profileVersion, &raw, &operator, &epoch)
	if err != nil {
		return definition.Definition{}, notFound("get definition", err)
	}
	config, err := decodeConfig(raw)
	if err != nil {
		return definition.Definition{}, err
	}
	return definition.Definition{
		Agent:        agent,
		Organization: organization,
		Revision:     definition.Revision(revision),
		Profile:      definition.ProfileRef{Name: profileName, Version: definition.Version(profileVersion)},
		Config:       config,
		Assignment:   assignmentOf(operator, epoch),
	}, nil
}

func (p *Postgres) BeginCreation(ctx context.Context, c definition.Creation) (definition.Creation, error) {
	b := c.Binding
	_, err := p.pool.Exec(ctx, beginCreation, c.Key.Organization, c.Key.RequestID, b.Actor, b.Operation, b.Target,
		b.BodySHA256, b.OperationRef, c.Controller, c.Template.Name, int64(c.Template.Version),
		c.Profile.Name, int64(c.Profile.Version))
	if err != nil {
		return definition.Creation{}, storeError("begin creation", err)
	}
	return scanCreation(p.pool.QueryRow(ctx, getCreation, c.Key.Organization, c.Key.RequestID), c.Key)
}

func (p *Postgres) RegisterCreation(
	ctx context.Context, key definition.RequestKey, d definition.Definition,
) (definition.Creation, bool, error) {
	if d.Assignment == nil {
		return definition.Creation{}, false, errors.New("register creation: the first revision records no assignment")
	}
	raw, err := encodeConfig(d.Config)
	if err != nil {
		return definition.Creation{}, false, storeError("encode configuration", err)
	}
	var (
		c          definition.Creation
		registered bool
	)
	err = p.inTx(ctx, func(tx pgx.Tx) error {
		c, err = scanCreation(tx.QueryRow(ctx, lockCreation, key.Organization, key.RequestID), key)
		if err != nil {
			return err
		}
		if _, pending := c.Outcome.(definition.Pending); !pending {
			return nil
		}
		operator, epoch := assignmentColumns(d.Assignment)
		_, err = tx.Exec(ctx, insertFirstDefinition, string(d.Agent), d.Organization, d.Profile.Name,
			int64(d.Profile.Version), raw, operator, epoch)
		if isUniqueViolation(err) {
			return definition.ErrStaleRevision
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, registerCreation, key.Organization, key.RequestID, string(d.Agent), *epoch); err != nil {
			return err
		}
		c.Outcome = definition.Registered{Agent: d.Agent, Epoch: *epoch}
		registered = true
		return nil
	})
	if err != nil {
		return definition.Creation{}, false, storeError("register creation", err)
	}
	return c, registered, nil
}

func (p *Postgres) FailCreation(ctx context.Context, key definition.RequestKey, failed definition.Failed) (definition.Creation, error) {
	if _, err := p.pool.Exec(ctx, failCreation, key.Organization, key.RequestID, failed.Reason, failed.Conflict); err != nil {
		return definition.Creation{}, storeError("fail creation", err)
	}
	return scanCreation(p.pool.QueryRow(ctx, getCreation, key.Organization, key.RequestID), key)
}

// scanCreation reads one creation row, the outcome from its state column.
func scanCreation(row pgx.Row, key definition.RequestKey) (definition.Creation, error) {
	var (
		c                               = definition.Creation{Key: key}
		templateVersion, profileVersion int64
		state                           string
		agent, epoch, reason            *string
		conflict                        bool
	)
	b := &c.Binding
	err := row.Scan(&b.Actor, &b.Operation, &b.Target, &b.BodySHA256, &b.OperationRef, &c.Controller,
		&c.Template.Name, &templateVersion, &c.Profile.Name, &profileVersion, &state, &agent, &epoch, &reason, &conflict)
	if err != nil {
		return definition.Creation{}, notFound("get creation", err)
	}
	c.Template.Version, c.Profile.Version = definition.Version(templateVersion), definition.Version(profileVersion)
	switch {
	case state == "pending":
		c.Outcome = definition.Pending{}
	case state == "registered" && agent != nil && epoch != nil:
		c.Outcome = definition.Registered{Agent: definition.GRN(*agent), Epoch: *epoch}
	case state == "failed" && reason != nil:
		c.Outcome = definition.Failed{Reason: *reason, Conflict: conflict}
	default:
		return definition.Creation{}, fmt.Errorf("creation in state %q violates the schema's checks", state)
	}
	return c, nil
}

func (p *Postgres) CreationOf(ctx context.Context, agent definition.GRN) (definition.Creation, error) {
	var key definition.RequestKey
	if err := p.pool.QueryRow(ctx, creationOfAgent, string(agent)).Scan(&key.Organization, &key.RequestID); err != nil {
		return definition.Creation{}, storeError("get creation of agent", notFound("get creation of agent", err))
	}
	return scanCreation(p.pool.QueryRow(ctx, getCreation, key.Organization, key.RequestID), key)
}

func (p *Postgres) BeginInitialCertificate(
	ctx context.Context, agent definition.GRN, r definition.CertificateRequest,
) (definition.InitialCertificate, error) {
	c, err := scanCertificate(p.pool.QueryRow(ctx, beginCertificate, string(agent), r.RequestID, r.Epoch, r.CSRPEM), agent)
	if err != nil {
		return definition.InitialCertificate{}, storeError("begin initial certificate", err)
	}
	return c, nil
}

func (p *Postgres) IssueInitialCertificate(
	ctx context.Context, agent definition.GRN, r definition.CertificateRequest, issued definition.IssuedCertificate,
) (definition.InitialCertificate, bool, error) {
	var (
		c        definition.InitialCertificate
		recorded bool
	)
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		c, err = scanCertificate(tx.QueryRow(ctx, lockCertificate, string(agent)), agent)
		if err != nil || c.Issued != nil || c.Request != r {
			return err
		}
		_, err = tx.Exec(ctx, issueCertificate, string(agent), issued.CertificatePEM, issued.IssuerPEM,
			issued.ServerRootPEM, issued.NotAfter)
		if err != nil {
			return err
		}
		c.Issued, recorded = &issued, true
		return nil
	})
	if err != nil {
		return definition.InitialCertificate{}, false, storeError("issue initial certificate", err)
	}
	return c, recorded, nil
}

func (p *Postgres) ClearInitialCertificate(ctx context.Context, agent definition.GRN, r definition.CertificateRequest) error {
	if _, err := p.pool.Exec(ctx, clearCertificate, string(agent), r.RequestID, r.Epoch, r.CSRPEM); err != nil {
		return storeError("clear initial certificate", err)
	}
	return nil
}

// scanCertificate reads one initial_certificates row, its result only once it is issued.
func scanCertificate(row pgx.Row, agent definition.GRN) (definition.InitialCertificate, error) {
	var (
		c                       = definition.InitialCertificate{Agent: agent}
		state                   string
		certificate, issuer, sr *string
		notAfter                *time.Time
	)
	err := row.Scan(&c.Request.RequestID, &c.Request.Epoch, &c.Request.CSRPEM, &state, &certificate, &issuer, &sr, &notAfter)
	if err != nil {
		return definition.InitialCertificate{}, notFound("get initial certificate", err)
	}
	switch {
	case state == "pending":
	case state == "issued" && certificate != nil && issuer != nil && sr != nil && notAfter != nil:
		c.Issued = &definition.IssuedCertificate{
			CertificatePEM: *certificate, IssuerPEM: *issuer, ServerRootPEM: *sr, NotAfter: notAfter.UTC(),
		}
	default:
		return definition.InitialCertificate{}, fmt.Errorf("initial certificate in state %q violates the schema's checks", state)
	}
	return c, nil
}

// placementAttempts bounds how often a registration is decided again after losing a race to
// another one for the same agent, which a unique violation reports.
const placementAttempts = 3

func (p *Postgres) RegisterPlacement(ctx context.Context, in definition.PlacementInput) (definition.Placement, bool, error) {
	for attempt := 1; ; attempt++ {
		placement, stored, err := p.registerPlacement(ctx, in)
		// Another registration for the agent committed between this one's reads and its write:
		// the decision is taken again, against what that one left.
		if isUniqueViolation(err) && attempt < placementAttempts {
			continue
		}
		if isPlacementRefusal(err) {
			return definition.Placement{}, false, err
		}
		if err != nil {
			return definition.Placement{}, false, storeError("register placement", err)
		}
		return placement, stored, nil
	}
}

func (p *Postgres) registerPlacement(ctx context.Context, in definition.PlacementInput) (definition.Placement, bool, error) {
	var (
		placement definition.Placement
		stored    bool
	)
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		current, err := scanPlacement(tx.QueryRow(ctx, lockCurrentPlacement, string(in.Agent)), in.Agent)
		if err != nil {
			return err
		}
		same, err := scanPlacement(tx.QueryRow(ctx, lockPodPlacement, string(in.Agent), in.Request.PodUID), in.Agent)
		if err != nil {
			return err
		}
		action, err := definition.DecidePlacement(current, same, in)
		if err != nil {
			return err
		}
		switch action {
		case definition.PlacementRepeat:
			placement = *same
			return nil
		case definition.PlacementRefresh:
			if _, err := tx.Exec(ctx, refreshPlacement, string(in.Agent), in.Request.PodUID, in.LeafDER); err != nil {
				return err
			}
			placement, placement.LeafDER = *same, in.LeafDER
			return nil
		}
		// Revoked first: the index admits one current placement per agent.
		if _, err := tx.Exec(ctx, revokePlacement, string(in.Agent)); err != nil {
			return err
		}
		r := in.Request
		if _, err := tx.Exec(ctx, insertPlacement, string(in.Agent), r.PodUID, in.Controller, r.Epoch, r.PVCUID,
			r.TokenSHA256, r.Previous.PodUID, r.Previous.WriterStoppedSHA256, in.LeafDER); err != nil {
			return err
		}
		placement = definition.Placement{Agent: in.Agent, Controller: in.Controller, Request: r, LeafDER: in.LeafDER}
		stored = true
		return nil
	})
	return placement, stored, err
}

// isPlacementRefusal reports whether err is DecidePlacement's refusal, which is the caller's to
// answer and is returned as it is.
func isPlacementRefusal(err error) bool {
	return errors.Is(err, definition.ErrPlacementSuperseded) || errors.Is(err, definition.ErrPreviousMismatch) ||
		errors.Is(err, definition.ErrEvidenceMissing) || errors.Is(err, definition.ErrPlacementConflict)
}

// scanPlacement reads one placements row, nil where there is none.
func scanPlacement(row pgx.Row, agent definition.GRN) (*definition.Placement, error) {
	p := definition.Placement{Agent: agent}
	r := &p.Request
	err := row.Scan(&p.Controller, &r.Epoch, &r.PVCUID, &r.TokenSHA256, &r.Previous.PodUID,
		&r.Previous.WriterStoppedSHA256, &p.LeafDER, &p.Superseded, &r.PodUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Postgres) CurrentPlacement(ctx context.Context, agent definition.GRN) (definition.Placement, error) {
	placement, err := scanPlacement(p.pool.QueryRow(ctx, currentPlacement, string(agent)), agent)
	if err != nil {
		return definition.Placement{}, storeError("get current placement", err)
	}
	if placement == nil {
		return definition.Placement{}, definition.ErrNotFound
	}
	return *placement, nil
}

// WithAgentLock holds a session advisory lock on one connection for as long as fn runs, so every
// instance of the service sharing the database waits on the same lock.
func (p *Postgres) WithAgentLock(ctx context.Context, agent definition.GRN, fn func(context.Context) error) error {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return storeError("acquire the activation lock's connection", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, lockAgent, string(agent)); err != nil {
		return storeError("take the agent's activation lock", err)
	}
	// Released on a context of its own: the request's may already be done.
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), unlockAgent, string(agent)) }()
	return fn(ctx)
}

func (p *Postgres) InsertActivation(ctx context.Context, a definition.Activation) (definition.Activation, error) {
	r := a.Request
	stored, err := scanActivation(p.pool.QueryRow(ctx, insertActivation, string(a.Agent), r.RequestID, r.Epoch, r.Generation,
		int64(r.ConfigRevision), r.PlacementPodUID, a.ReplacesActivationID, a.OperationRef), a.Agent)
	if err != nil {
		return definition.Activation{}, storeError("insert activation request", err)
	}
	return stored, nil
}

func (p *Postgres) GetActivation(ctx context.Context, agent definition.GRN, requestID string) (definition.Activation, error) {
	stored, err := scanActivation(p.pool.QueryRow(ctx, getActivation, string(agent), requestID), agent)
	if err != nil {
		return definition.Activation{}, storeError("get activation request", notFound("get activation request", err))
	}
	return stored, nil
}

func (p *Postgres) RecordActivation(ctx context.Context, agent definition.GRN, requestID, activationID string) error {
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, recordActivation, string(agent), requestID, activationID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return definition.ErrActivationMismatch
		}
		_, err = tx.Exec(ctx, recordLatestActivation, string(agent), activationID)
		return err
	})
	if errors.Is(err, definition.ErrActivationMismatch) {
		return err
	}
	if err != nil {
		return storeError("record activation", err)
	}
	return nil
}

func (p *Postgres) LatestActivation(ctx context.Context, agent definition.GRN) (string, error) {
	var latest string
	if err := p.pool.QueryRow(ctx, latestActivation, string(agent)).Scan(&latest); err != nil {
		return "", storeError("get latest activation", notFound("get latest activation", err))
	}
	return latest, nil
}

func (p *Postgres) ActivationOfGeneration(ctx context.Context, agent definition.GRN, generation string) (string, error) {
	var activation string
	if err := p.pool.QueryRow(ctx, activationOfGeneration, string(agent), generation).Scan(&activation); err != nil {
		return "", storeError("get activation of generation", notFound("get activation of generation", err))
	}
	return activation, nil
}

func (p *Postgres) ConfigureReference(ctx context.Context, agent definition.GRN, revision definition.Revision) (string, error) {
	var ref string
	if err := p.pool.QueryRow(ctx, configureReference, string(agent), int64(revision)).Scan(&ref); err != nil {
		return "", storeError("get configure reference", notFound("get configure reference", err))
	}
	return ref, nil
}

func (p *Postgres) RecordRuntimeApplied(ctx context.Context, agent definition.GRN, applied definition.RuntimeApplied) error {
	if _, err := p.pool.Exec(ctx, recordRuntimeApplied, string(agent), int64(applied.Revision), applied.ActivationID,
		applied.Generation, applied.ObservedAt); err != nil {
		return storeError("record runtime applied", err)
	}
	return nil
}

func (p *Postgres) GetRuntimeApplied(ctx context.Context, agent definition.GRN) (definition.RuntimeApplied, error) {
	var applied definition.RuntimeApplied
	var revision int64
	err := p.pool.QueryRow(ctx, getRuntimeApplied, string(agent)).
		Scan(&revision, &applied.ActivationID, &applied.Generation, &applied.ObservedAt)
	if err != nil {
		return definition.RuntimeApplied{}, storeError("get runtime applied", notFound("get runtime applied", err))
	}
	applied.Revision = definition.Revision(revision)
	return applied, nil
}

// scanActivation reads one activation_requests row.
func scanActivation(row pgx.Row, agent definition.GRN) (definition.Activation, error) {
	a := definition.Activation{Agent: agent}
	r := &a.Request
	var revision int64
	if err := row.Scan(&r.RequestID, &r.Epoch, &r.Generation, &revision, &r.PlacementPodUID,
		&a.ReplacesActivationID, &a.OperationRef, &a.ActivationID); err != nil {
		return definition.Activation{}, err
	}
	r.ConfigRevision = definition.Revision(revision)
	return a, nil
}

// cutoverAttempts bounds how often an import is decided again after losing a race to another
// import of the same agent, which a unique violation reports.
const cutoverAttempts = 3

func (p *Postgres) BeginCutoverImport(
	ctx context.Context, imp definition.CutoverImport, d definition.Definition,
) (definition.CutoverImport, bool, error) {
	for attempt := 1; ; attempt++ {
		stored, first, err := p.beginCutoverImport(ctx, imp, d)
		if isUniqueViolation(err) && attempt < cutoverAttempts {
			continue
		}
		if errors.Is(err, definition.ErrAlreadyDefined) {
			return definition.CutoverImport{}, false, err
		}
		if err != nil {
			return definition.CutoverImport{}, false, storeError("begin cutover import", err)
		}
		return stored, first, nil
	}
}

func (p *Postgres) beginCutoverImport(
	ctx context.Context, imp definition.CutoverImport, d definition.Definition,
) (definition.CutoverImport, bool, error) {
	var (
		stored definition.CutoverImport
		first  bool
	)
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		existing, err := scanCutover(tx.QueryRow(ctx, lockCutover, string(imp.Agent)), imp.Agent)
		if err == nil {
			stored = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, definitionExists, string(imp.Agent), imp.Organization).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return definition.ErrAlreadyDefined
		}
		raw, err := encodeConfig(d.Config)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, insertFirstDefinition, string(d.Agent), d.Organization, d.Profile.Name,
			int64(d.Profile.Version), raw, nil, nil); err != nil {
			return err
		}
		values, dispositions, pins, err := cutoverJSON(imp)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, insertCutover, string(imp.Agent), imp.Organization, imp.ImportID, imp.Epoch, imp.Assignee, imp.SourceDigest,
			values, dispositions, imp.Profile.Name, int64(imp.Profile.Version), pins); err != nil {
			return err
		}
		stored, first = imp, true
		return nil
	})
	return stored, first, err
}

func (p *Postgres) GetCutoverImport(ctx context.Context, agent definition.GRN) (definition.CutoverImport, error) {
	imp, err := scanCutover(p.pool.QueryRow(ctx, getCutover, string(agent)), agent)
	if err != nil {
		return definition.CutoverImport{}, storeError("get cutover import", notFound("get cutover import", err))
	}
	return imp, nil
}

func (p *Postgres) FreezeCutoverImport(ctx context.Context, agent definition.GRN, importID string) error {
	return p.cutoverStage(ctx, agent, importID, func(tx pgx.Tx, imp definition.CutoverImport) error {
		if imp.Stage != definition.CutoverImported && imp.Stage != definition.CutoverFrozen {
			return definition.ErrCutoverStage
		}
		_, err := tx.Exec(ctx, setCutoverStage, string(agent), string(definition.CutoverFrozen), "")
		return err
	})
}

func (p *Postgres) SwitchCutoverImport(ctx context.Context, agent definition.GRN, importID, configureRef string) error {
	return p.cutoverStage(ctx, agent, importID, func(tx pgx.Tx, imp definition.CutoverImport) error {
		switch imp.Stage {
		case definition.CutoverSwitched:
			return nil
		case definition.CutoverFrozen:
		default:
			return definition.ErrCutoverStage
		}
		if _, err := tx.Exec(ctx, setCutoverStage, string(agent), string(definition.CutoverSwitched), configureRef); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, activateImportedRevision, string(agent), imp.Assignee, imp.Epoch)
		return err
	})
}

func (p *Postgres) DiscardCutoverImport(ctx context.Context, agent definition.GRN, importID string) error {
	return p.cutoverStage(ctx, agent, importID, func(tx pgx.Tx, imp definition.CutoverImport) error {
		if imp.Stage == definition.CutoverSwitched {
			return definition.ErrReverseMigrationRequired
		}
		if _, err := tx.Exec(ctx, deleteCutover, string(agent)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, deleteImportedRevisions, string(agent))
		return err
	})
}

// cutoverStage runs apply on the agent's import under importID, locked, in one transaction.
func (p *Postgres) cutoverStage(ctx context.Context, agent definition.GRN, importID string,
	apply func(pgx.Tx, definition.CutoverImport) error) error {
	err := p.inTx(ctx, func(tx pgx.Tx) error {
		imp, err := scanCutover(tx.QueryRow(ctx, lockCutover, string(agent)), agent)
		if errors.Is(err, pgx.ErrNoRows) {
			return definition.ErrNotFound
		}
		if err != nil {
			return err
		}
		if imp.ImportID != importID {
			return definition.ErrImportOpen
		}
		return apply(tx, imp)
	})
	for _, refusal := range []error{definition.ErrNotFound, definition.ErrImportOpen, definition.ErrCutoverStage,
		definition.ErrReverseMigrationRequired} {
		if errors.Is(err, refusal) {
			return refusal
		}
	}
	if err != nil {
		return storeError("record cutover stage", err)
	}
	return nil
}

// cutoverJSON is the import's values, dispositions and pins as their columns hold them.
func cutoverJSON(imp definition.CutoverImport) (values, dispositions, pins []byte, err error) {
	if values, err = json.Marshal(nonNil(imp.Values)); err != nil {
		return nil, nil, nil, err
	}
	if dispositions, err = json.Marshal(imp.Dispositions); err != nil {
		return nil, nil, nil, err
	}
	if imp.Dispositions == nil {
		dispositions = []byte("{}")
	}
	if pins, err = json.Marshal(nonNil(imp.Pins)); err != nil {
		return nil, nil, nil, err
	}
	return values, dispositions, pins, nil
}

func nonNil[M ~map[string]string](m M) M {
	if m == nil {
		return M{}
	}
	return m
}

// scanCutover reads one cutover_imports row.
func scanCutover(row pgx.Row, agent definition.GRN) (definition.CutoverImport, error) {
	imp := definition.CutoverImport{Agent: agent}
	var (
		values, dispositions, pins []byte
		version                    int64
		stage                      string
	)
	if err := row.Scan(&imp.Organization, &imp.ImportID, &imp.Epoch, &imp.Assignee, &imp.SourceDigest, &values, &dispositions,
		&imp.Profile.Name, &version, &pins, &stage, &imp.ConfigureRef); err != nil {
		return definition.CutoverImport{}, err
	}
	imp.Profile.Version, imp.Stage = definition.Version(version), definition.CutoverStage(stage)
	for raw, into := range map[*[]byte]any{&values: &imp.Values, &dispositions: &imp.Dispositions, &pins: &imp.Pins} {
		if err := json.Unmarshal(*raw, into); err != nil {
			return definition.CutoverImport{}, fmt.Errorf("decode cutover import: %v", err)
		}
	}
	return imp, nil
}

// inTx runs fn in one transaction, committed when fn returns nil.
func (p *Postgres) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, p.pool, fn)
}

// notFound translates a missing row into definition.ErrNotFound.
func notFound(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return definition.ErrNotFound
	}
	return storeError(op, err)
}

// storeError passes a domain sentinel through and keeps any other error, the
// driver's included, opaque to the caller.
func storeError(op string, err error) error {
	if errors.Is(err, definition.ErrNotFound) || errors.Is(err, definition.ErrStaleRevision) {
		return err
	}
	return fmt.Errorf("%s: %v", op, err)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}
