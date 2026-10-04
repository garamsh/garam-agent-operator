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

// uniqueViolation is PostgreSQL's SQLSTATE for a duplicate key.
const uniqueViolation = "23505"

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

	for _, table := range []string{"profiles", "templates", "definitions", "creations"} {
		var exists bool
		require.NoError(t, pool.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists))
		assert.True(t, exists, "table %s", table)
	}
}

func TestSchema_SecondRevisionUnderOneNumberRefused(t *testing.T) {
	profile := publishProfile(t)
	agent := name(t, "agent")
	insert := func(revision int) error {
		return execute(t, `INSERT INTO definitions (agent, revision, profile_name, profile_version, config)
VALUES ($1, $2, $3, 1, '{}')`, agent, revision, profile)
	}

	// Control: the first revision under each number is accepted.
	require.NoError(t, insert(1))
	require.NoError(t, insert(2))

	assertUniqueViolation(t, insert(2))
}

func TestSchema_SecondCreationUnderOneKeyRefused(t *testing.T) {
	profile := publishProfile(t)
	template := name(t, "template")
	require.NoError(t, execute(t, `INSERT INTO templates (name, version, profile_name, profile_version, config)
VALUES ($1, 1, $2, 1, '{}')`, template, profile))
	organization := name(t, "organization")
	insert := func(requestID string) error {
		return execute(t, `INSERT INTO creations (actor, organization, request_id, template_name, template_version, state)
VALUES ('grn:acme:default:user:7c1d', $1, $2, $3, 1, 'pending')`, organization, requestID, template)
	}

	// Control: a first creation under a key, and one under another key, are accepted.
	require.NoError(t, insert("r1"))
	require.NoError(t, insert("r2"))

	assertUniqueViolation(t, insert("r1"))
}

// publishProfile stores version 1 of a profile named for the test and returns its name.
func publishProfile(t *testing.T) string {
	t.Helper()
	profile := name(t, "profile")
	require.NoError(t, execute(t, `INSERT INTO profiles (name, version, settings) VALUES ($1, 1, '{}')`, profile))
	return profile
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
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "want a PostgreSQL error, got %v", err)
	assert.Equal(t, uniqueViolation, pgErr.Code, pgErr.Message)
}
