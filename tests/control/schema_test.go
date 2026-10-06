//go:build e2e

package control_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// uniqueViolation is PostgreSQL's SQLSTATE for a duplicate key.
	uniqueViolation = "23505"
	// foreignKeyViolation is PostgreSQL's SQLSTATE for a reference to a row that does not exist.
	foreignKeyViolation = "23503"
)

var (
	// runID keeps one process's rows apart from an earlier process's in the same database.
	runID = time.Now().UnixNano()
	// names numbers every name handed out, so a test repeated in one process takes new ones.
	names atomic.Int64
)

func TestBinary_ServesHealthOnTheSchemaItApplied(t *testing.T) {
	resp, err := http.Get(healthURL + "/healthz")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	tables := []string{"positions", "profiles", "templates", "publications", "definitions", "creations", "requests",
		"agent_status"}
	for _, table := range tables {
		var exists bool
		require.NoError(t, pool.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists))
		assert.True(t, exists, "table %s", table)
	}
}

func TestSchema_SecondRevisionUnderOneNumberRefused(t *testing.T) {
	organization := name(t, "organization")
	profile := publishProfile(t, organization)
	agent := name(t, "agent")
	insert := func(revision int) error {
		return insertDefinition(t, agent, organization, revision, profile)
	}

	// Control: the first revision under each number is accepted.
	require.NoError(t, insert(1))
	require.NoError(t, insert(2))

	assertUniqueViolation(t, insert(2))
}

func TestSchema_SecondCreationUnderOneKeyRefused(t *testing.T) {
	organization := name(t, "organization")
	template, profile := name(t, "template"), publishProfile(t, organization)
	require.NoError(t, insertTemplate(t, organization, template, profile))
	insert := func(requestID, actor string) error {
		return insertCreation(t, organization, requestID, actor, template, profile)
	}

	// Control: a first creation under a key, and one under another key, are accepted.
	require.NoError(t, insert("r1", "grn:acme:default:user:7c1d"))
	require.NoError(t, insert("r2", "grn:acme:default:user:7c1d"))

	// The key is the organization and request id alone: another actor does not make another key.
	assertUniqueViolation(t, insert("r1", "grn:acme:default:user:other"))
}

func TestSchema_SecondConfigureRequestUnderOneKeyRefused(t *testing.T) {
	organization := name(t, "organization")
	insert := func(requestID, actor string) error {
		return execute(t, `INSERT INTO requests (organization, request_id, actor, operation, target, body_sha256,
    operation_ref, assignment_operator, assignment_epoch, agent, outcome)
VALUES ($1, $2, $3, 'agent:configure', 'agent', 'digest', 'ref', 'operator', '7', 'agent', 'stale')`,
			organization, requestID, actor)
	}

	// Control: a first request under a key, and one under another key, are accepted.
	require.NoError(t, insert("r1", "grn:acme:default:user:7c1d"))
	require.NoError(t, insert("r2", "grn:acme:default:user:7c1d"))

	assertUniqueViolation(t, insert("r1", "grn:acme:default:user:other"))
}

func TestSchema_TwoOrganizationsHoldOneNameAndVersion(t *testing.T) {
	first, second := name(t, "organization"), name(t, "organization")
	profile, template := name(t, "profile"), name(t, "template")

	// Each organization's version 1 of one profile name, and of one template name, is accepted.
	require.NoError(t, insertProfile(t, first, profile))
	require.NoError(t, insertProfile(t, second, profile))
	require.NoError(t, insertTemplate(t, first, template, profile))
	require.NoError(t, insertTemplate(t, second, template, profile))

	// Control: a second version 1 within one organization is refused.
	assertUniqueViolation(t, insertProfile(t, first, profile))
	assertUniqueViolation(t, insertTemplate(t, first, template, profile))
}

func TestSchema_ReferenceToAnotherOrganizationsRowRefused(t *testing.T) {
	tests := []struct {
		name string
		// setup gives other everything the row names but the one reference under test.
		setup func(t *testing.T, other, profile, template string)
		// insert stores a row of organization naming the profile and template published in published.
		insert func(t *testing.T, organization, profile, template string) error
	}{
		{"a template naming a profile", nil, func(t *testing.T, organization, profile, _ string) error {
			return insertTemplate(t, organization, name(t, "template"), profile)
		}},
		{"a definition naming a profile", nil, func(t *testing.T, organization, profile, _ string) error {
			return insertDefinition(t, name(t, "agent"), organization, 1, profile)
		}},
		{"a creation naming a template", func(t *testing.T, other, profile, _ string) {
			publishProfileNamed(t, other, profile)
		}, insertCreationNaming},
		{"a creation naming a profile", func(t *testing.T, other, _, template string) {
			own := publishProfile(t, other)
			require.NoError(t, insertTemplate(t, other, template, own))
		}, insertCreationNaming},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			published, other := name(t, "organization"), name(t, "organization")
			profile, template := publishProfile(t, published), name(t, "template")
			require.NoError(t, insertTemplate(t, published, template, profile))
			if tt.setup != nil {
				tt.setup(t, other, profile, template)
			}

			assertForeignKeyViolation(t, tt.insert(t, other, profile, template))

			// Control: the same row in the organization that published the name is accepted.
			require.NoError(t, tt.insert(t, published, profile, template))
		})
	}
}

// insertProfile stores version 1 of organization's profile by SQL, for the schema's own key test:
// the binary's publish-profile answers that row's repeat as unchanged rather than storing it.
func insertProfile(t *testing.T, organization, profile string) error {
	t.Helper()
	return execute(t, `INSERT INTO profiles (organization, name, version, settings) VALUES ($1, $2, 1, '{}')`,
		organization, profile)
}

// insertTemplate stores version 1 of organization's template, naming version 1 of profile.
func insertTemplate(t *testing.T, organization, template, profile string) error {
	t.Helper()
	return execute(t, `INSERT INTO templates (organization, name, version, profile_name, profile_version, config)
VALUES ($1, $2, 1, $3, 1, '{}')`, organization, template, profile)
}

// insertDefinition stores revision of agent, in organization, naming version 1 of profile.
func insertDefinition(t *testing.T, agent, organization string, revision int, profile string) error {
	t.Helper()
	return execute(t, `WITH next AS (UPDATE positions SET position = position + 1 RETURNING position)
INSERT INTO definitions (agent, organization, revision, profile_name, profile_version, config, position)
SELECT $1::text, $2::text, $3::bigint, $4::text, 1, '{}', (SELECT position FROM next)`,
		agent, organization, revision, profile)
}

// insertCreation stores a pending creation in organization from version 1 of template under
// version 1 of profile.
func insertCreation(t *testing.T, organization, requestID, actor, template, profile string) error {
	t.Helper()
	return execute(t, `INSERT INTO creations (actor, organization, request_id, operation, target, body_sha256,
    operation_ref, controller, template_name, template_version, profile_name, profile_version, state)
VALUES ($1, $2, $3, 'agent:create', 'k8s', 'digest', 'ref', 'k8s', $4, 1, $5, 1, 'pending')`,
		actor, organization, requestID, template, profile)
}

// insertCreationNaming stores a creation of organization naming version 1 of profile and of template.
func insertCreationNaming(t *testing.T, organization, profile, template string) error {
	t.Helper()
	return insertCreation(t, organization, name(t, "request"), "grn:acme:default:user:7c1d", template, profile)
}

func name(t *testing.T, kind string) string {
	return fmt.Sprintf("%s-%s-%d-%d", t.Name(), kind, runID, names.Add(1))
}

func execute(t *testing.T, query string, args ...any) error {
	t.Helper()
	_, err := pool.Exec(context.Background(), query, args...)
	return err
}

func assertUniqueViolation(t *testing.T, err error) {
	t.Helper()
	assertViolation(t, uniqueViolation, err)
}

func assertForeignKeyViolation(t *testing.T, err error) {
	t.Helper()
	assertViolation(t, foreignKeyViolation, err)
}

func assertViolation(t *testing.T, code string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "want a PostgreSQL error, got %v", err)
	assert.Equal(t, code, pgErr.Code, pgErr.Message)
}

func TestSchema_SecondCurrentPlacementOfOneAgentRefused(t *testing.T) {
	agent := name(t, "agent")
	insert := func(pod string) error {
		return execute(t, `INSERT INTO placements (agent, pod_uid, controller, epoch, pvc_uid, token_sha256, leaf_der)
VALUES ($1, $2, 'controller', '1', 'pvc', 'token', '\x00')`, agent, pod)
	}

	require.NoError(t, insert("pod-1"))
	assertUniqueViolation(t, insert("pod-2"))

	// Control: once the first is revoked, the next is current.
	require.NoError(t, execute(t, "UPDATE placements SET revoked_at = now() WHERE agent = $1", agent))
	require.NoError(t, insert("pod-2"))
}
