package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStorage implements the Storage interface
type PostgresStorage struct {
	db *pgxpool.Pool
}

func (p *PostgresStorage) GetState(ctx context.Context, customerID string) (string, error) {
	upsertQuery := `
		INSERT INTO customers (chat_id, current_state)
		VALUES($1, 'idle')
		ON CONFLICT (chat_id) DO NOTHING;
	`
	_, err := p.db.Exec(ctx, upsertQuery, customerID)
	if err != nil {
		return "", err
	}

	var state string
	fetchStateQuery := `SELECT current_state FROM customers WHERE chat_id = $1;`
	err = p.db.QueryRow(ctx, fetchStateQuery, customerID).Scan(&state)
	return state, err
}

func (p *PostgresStorage) UpdateState(ctx context.Context, customerID string, newState string) error {
	updateQuery := `UPDATE customers SET current_state = $1 WHERE chat_id = $2;`
	_, err := p.db.Exec(ctx, updateQuery, newState, customerID)
	return err
}
