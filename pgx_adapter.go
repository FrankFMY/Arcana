package arcana

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgxQuerier adapts a pgxpool.Pool to the Querier interface.
// pgx.Rows and pgx.Row structurally satisfy arcana.Rows and arcana.Row.
func PgxQuerier(pool *pgxpool.Pool) Querier {
	return &pgxAdapter{pool: pool}
}

type pgxAdapter struct {
	pool *pgxpool.Pool
}

func (a *pgxAdapter) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	rows, err := a.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (a *pgxAdapter) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return a.pool.QueryRow(ctx, sql, args...)
}
