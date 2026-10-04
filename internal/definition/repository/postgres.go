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

func (p *Postgres) AppendDefinition(ctx context.Context, d definition.Definition) error {
	raw, err := encodeConfig(d.Config)
	if err != nil {
		return storeError("encode configuration", err)
	}
	tag, err := p.pool.Exec(ctx, appendDefinition,
		string(d.Agent), int64(d.Revision), d.Profile.Name, int64(d.Profile.Version), raw)
	if isUniqueViolation(err) {
		return definition.ErrStaleRevision
	}
	if err != nil {
		return storeError("append definition", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := p.pool.QueryRow(ctx, definitionExists, string(d.Agent)).Scan(&exists); err != nil {
		return storeError("append definition", err)
	}
	if !exists {
		return definition.ErrNotFound
	}
	return definition.ErrStaleRevision
}

func (p *Postgres) GetDefinition(ctx context.Context, agent definition.GRN) (definition.Definition, error) {
	var (
		revision       int64
		profileName    string
		profileVersion int64
		raw            []byte
	)
	err := p.pool.QueryRow(ctx, getDefinition, string(agent)).Scan(&revision, &profileName, &profileVersion, &raw)
	if err != nil {
		return definition.Definition{}, notFound("get definition", err)
	}
	config, err := decodeConfig(raw)
	if err != nil {
		return definition.Definition{}, err
	}
	return definition.Definition{
		Agent:    agent,
		Revision: definition.Revision(revision),
		Profile:  definition.ProfileRef{Name: profileName, Version: definition.Version(profileVersion)},
		Config:   config,
	}, nil
}

func (p *Postgres) BeginCreation(ctx context.Context, c definition.Creation) (definition.Creation, error) {
	_, err := p.pool.Exec(ctx, beginCreation,
		c.Key.Actor, c.Key.Organization, c.Key.RequestID, c.Template.Name, int64(c.Template.Version))
	if err != nil {
		return definition.Creation{}, storeError("begin creation", err)
	}
	return scanCreation(p.pool.QueryRow(ctx, getCreation, c.Key.Actor, c.Key.Organization, c.Key.RequestID), c.Key)
}

func (p *Postgres) RegisterCreation(ctx context.Context, key definition.CreationKey, d definition.Definition) (definition.Creation, error) {
	raw, err := encodeConfig(d.Config)
	if err != nil {
		return definition.Creation{}, storeError("encode configuration", err)
	}
	var c definition.Creation
	err = p.inTx(ctx, func(tx pgx.Tx) error {
		c, err = scanCreation(tx.QueryRow(ctx, lockCreation, key.Actor, key.Organization, key.RequestID), key)
		if err != nil {
			return err
		}
		if _, pending := c.Outcome.(definition.Pending); !pending {
			return nil
		}
		_, err = tx.Exec(ctx, insertFirstDefinition, string(d.Agent), d.Profile.Name, int64(d.Profile.Version), raw)
		if isUniqueViolation(err) {
			return definition.ErrStaleRevision
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, registerCreation, key.Actor, key.Organization, key.RequestID, string(d.Agent)); err != nil {
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

func (p *Postgres) FailCreation(ctx context.Context, key definition.CreationKey, reason string) (definition.Creation, error) {
	if _, err := p.pool.Exec(ctx, failCreation, key.Actor, key.Organization, key.RequestID, reason); err != nil {
		return definition.Creation{}, storeError("fail creation", err)
	}
	return scanCreation(p.pool.QueryRow(ctx, getCreation, key.Actor, key.Organization, key.RequestID), key)
}

// scanCreation reads one creation row, the outcome from its state column.
func scanCreation(row pgx.Row, key definition.CreationKey) (definition.Creation, error) {
	var (
		templateName    string
		templateVersion int64
		state           string
		agent, reason   *string
	)
	if err := row.Scan(&templateName, &templateVersion, &state, &agent, &reason); err != nil {
		return definition.Creation{}, notFound("get creation", err)
	}
	c := definition.Creation{
		Key:      key,
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
