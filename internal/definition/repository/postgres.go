package repository

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// Postgres is the control service's PostgreSQL database. It holds the schema only;
// the methods that store and read desired state arrive with issue #211.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres returns a Postgres on pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// ApplySchema creates whatever part of the schema the database does not hold yet.
func (p *Postgres) ApplySchema(ctx context.Context) error {
	if _, err := p.pool.Exec(ctx, schema); err != nil {
		return fmt.Errorf("apply schema: %v", err)
	}
	return nil
}
