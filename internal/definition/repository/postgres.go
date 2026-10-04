package repository

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

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
	Resources        corev1.ResourceRequirements `json:"resources"`
	StorageSize      resource.Quantity           `json:"storageSize"`
	StorageClassName *string                     `json:"storageClassName,omitempty"`
}

// configColumn is a configuration as stored.
type configColumn struct {
	Provider  string            `json:"provider"`
	BaseURL   string            `json:"baseURL"`
	ModelName string            `json:"modelName"`
	APIKeyRef string            `json:"apiKeyRef"`
	Ego       string            `json:"ego"`
	Tools     map[string]string `json:"tools"`
}

func encodeConfig(c definition.Configuration) ([]byte, error) {
	return json.Marshal(configColumn{
		Provider:  c.Model.Provider,
		BaseURL:   c.Model.BaseURL,
		ModelName: c.Model.Name,
		APIKeyRef: string(c.Model.APIKey),
		Ego:       c.Ego,
		Tools:     c.Tools,
	})
}

func decodeConfig(raw []byte) (definition.Configuration, error) {
	var c configColumn
	if err := json.Unmarshal(raw, &c); err != nil {
		return definition.Configuration{}, storeError("decode configuration", err)
	}
	return definition.Configuration{
		Model: definition.Model{
			Provider: c.Provider,
			BaseURL:  c.BaseURL,
			Name:     c.ModelName,
			APIKey:   definition.SecretRef(c.APIKeyRef),
		},
		Ego:   c.Ego,
		Tools: c.Tools,
	}, nil
}

func (p *Postgres) PublishProfile(ctx context.Context, name string, settings definition.ExecutionSettings) (definition.Profile, error) {
	raw, err := json.Marshal(settingsColumn(settings))
	if err != nil {
		return definition.Profile{}, storeError("encode settings", err)
	}
	var version int64
	err = p.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockProfiles); err != nil {
			return err
		}
		return tx.QueryRow(ctx, publishProfile, name, raw).Scan(&version)
	})
	if err != nil {
		return definition.Profile{}, storeError("publish profile", err)
	}
	return p.GetProfile(ctx, definition.ProfileRef{Name: name, Version: definition.Version(version)})
}

func (p *Postgres) GetProfile(ctx context.Context, ref definition.ProfileRef) (definition.Profile, error) {
	var raw []byte
	if err := p.pool.QueryRow(ctx, getProfile, ref.Name, int64(ref.Version)).Scan(&raw); err != nil {
		return definition.Profile{}, notFound("get profile", err)
	}
	var s settingsColumn
	if err := json.Unmarshal(raw, &s); err != nil {
		return definition.Profile{}, storeError("decode settings", err)
	}
	return definition.Profile{Name: ref.Name, Version: ref.Version, Settings: definition.ExecutionSettings(s)}, nil
}

func (p *Postgres) PublishTemplate(ctx context.Context, t definition.Template) (definition.Template, error) {
	raw, err := encodeConfig(t.Config)
	if err != nil {
		return definition.Template{}, storeError("encode configuration", err)
	}
	var version int64
	err = p.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockTemplates); err != nil {
			return err
		}
		return tx.QueryRow(ctx, publishTemplate, t.Name, t.Profile.Name, int64(t.Profile.Version), raw).Scan(&version)
	})
	if err != nil {
		return definition.Template{}, storeError("publish template", err)
	}
	return p.GetTemplate(ctx, definition.TemplateRef{Name: t.Name, Version: definition.Version(version)})
}

func (p *Postgres) GetTemplate(ctx context.Context, ref definition.TemplateRef) (definition.Template, error) {
	var (
		profileName    string
		profileVersion int64
		raw            []byte
	)
	err := p.pool.QueryRow(ctx, getTemplate, ref.Name, int64(ref.Version)).Scan(&profileName, &profileVersion, &raw)
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
		if err := tx.QueryRow(ctx, definitionExists, string(d.Agent)).Scan(&exists); err != nil {
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
			string(d.Agent), int64(d.Revision), d.Profile.Name, int64(d.Profile.Version), raw, operator, epoch)
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
		agent                 string
		revision, profileVers int64
		profileName           string
		config, settings      []byte
		epoch                 string
	)
	if err := row.Scan(&agent, &revision, &profileName, &profileVers, &config, &epoch, &settings); err != nil {
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
			Agent:      definition.GRN(agent),
			Revision:   definition.Revision(revision),
			Profile:    definition.ProfileRef{Name: profileName, Version: definition.Version(profileVers)},
			Config:     c,
			Assignment: &definition.Assignment{Operator: operator, Epoch: epoch},
		},
		Settings: definition.ExecutionSettings(s),
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
		revision        int64
		profileName     string
		profileVersion  int64
		raw             []byte
		operator, epoch *string
	)
	err := p.pool.QueryRow(ctx, getDefinition, string(agent)).
		Scan(&revision, &profileName, &profileVersion, &raw, &operator, &epoch)
	if err != nil {
		return definition.Definition{}, notFound("get definition", err)
	}
	config, err := decodeConfig(raw)
	if err != nil {
		return definition.Definition{}, err
	}
	return definition.Definition{
		Agent:      agent,
		Revision:   definition.Revision(revision),
		Profile:    definition.ProfileRef{Name: profileName, Version: definition.Version(profileVersion)},
		Config:     config,
		Assignment: assignmentOf(operator, epoch),
	}, nil
}

func (p *Postgres) BeginCreation(ctx context.Context, c definition.Creation) (definition.Creation, error) {
	_, err := p.pool.Exec(ctx, beginCreation,
		c.Key.Organization, c.Key.RequestID, c.Actor, c.Template.Name, int64(c.Template.Version))
	if err != nil {
		return definition.Creation{}, storeError("begin creation", err)
	}
	return scanCreation(p.pool.QueryRow(ctx, getCreation, c.Key.Organization, c.Key.RequestID), c.Key)
}

func (p *Postgres) RegisterCreation(ctx context.Context, key definition.RequestKey, d definition.Definition) (definition.Creation, error) {
	raw, err := encodeConfig(d.Config)
	if err != nil {
		return definition.Creation{}, storeError("encode configuration", err)
	}
	var c definition.Creation
	err = p.inTx(ctx, func(tx pgx.Tx) error {
		c, err = scanCreation(tx.QueryRow(ctx, lockCreation, key.Organization, key.RequestID), key)
		if err != nil {
			return err
		}
		if _, pending := c.Outcome.(definition.Pending); !pending {
			return nil
		}
		operator, epoch := assignmentColumns(d.Assignment)
		_, err = tx.Exec(ctx, insertFirstDefinition, string(d.Agent), d.Profile.Name, int64(d.Profile.Version), raw,
			operator, epoch)
		if isUniqueViolation(err) {
			return definition.ErrStaleRevision
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, registerCreation, key.Organization, key.RequestID, string(d.Agent)); err != nil {
			return err
		}
		c.Outcome = definition.Registered{Agent: d.Agent}
		return nil
	})
	if err != nil {
		return definition.Creation{}, storeError("register creation", err)
	}
	return c, nil
}

func (p *Postgres) FailCreation(ctx context.Context, key definition.RequestKey, reason string) (definition.Creation, error) {
	if _, err := p.pool.Exec(ctx, failCreation, key.Organization, key.RequestID, reason); err != nil {
		return definition.Creation{}, storeError("fail creation", err)
	}
	return scanCreation(p.pool.QueryRow(ctx, getCreation, key.Organization, key.RequestID), key)
}

// scanCreation reads one creation row, the outcome from its state column.
func scanCreation(row pgx.Row, key definition.RequestKey) (definition.Creation, error) {
	var (
		actor           string
		templateName    string
		templateVersion int64
		state           string
		agent, reason   *string
	)
	if err := row.Scan(&actor, &templateName, &templateVersion, &state, &agent, &reason); err != nil {
		return definition.Creation{}, notFound("get creation", err)
	}
	c := definition.Creation{
		Key:      key,
		Actor:    actor,
		Template: definition.TemplateRef{Name: templateName, Version: definition.Version(templateVersion)},
	}
	switch {
	case state == "pending":
		c.Outcome = definition.Pending{}
	case state == "registered" && agent != nil:
		c.Outcome = definition.Registered{Agent: definition.GRN(*agent)}
	case state == "failed" && reason != nil:
		c.Outcome = definition.Failed{Reason: *reason}
	default:
		return definition.Creation{}, fmt.Errorf("creation in state %q violates the schema's checks", state)
	}
	return c, nil
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
