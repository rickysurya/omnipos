package main

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStorage struct {
	db *pgxpool.Pool
}

func (p *PostgresStorage) GetState(ctx context.Context, customerID string) (string, error) {
	upsertQuery := `
		INSERT INTO customers (chat_id, current_state)
		VALUES($1, 'idle')
		ON CONFLICT (chat_id) DO NOTHING;
	`
	if _, err := p.db.Exec(ctx, upsertQuery, customerID); err != nil {
		return "", err
	}

	var state string
	err := p.db.QueryRow(ctx, `SELECT current_state FROM customers WHERE chat_id = $1;`, customerID).Scan(&state)
	return state, err
}

func (p *PostgresStorage) UpdateState(ctx context.Context, customerID string, newState string) error {
	_, err := p.db.Exec(ctx, `UPDATE customers SET current_state = $1 WHERE chat_id = $2;`, newState, customerID)
	return err
}

func (p *PostgresStorage) AddItemToOrder(ctx context.Context, customerID string, item string) error {
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var orderID int
	err = tx.QueryRow(ctx, `SELECT id FROM orders WHERE customer_id = $1 AND status = 'pending' LIMIT 1`, customerID).Scan(&orderID)

	if err == pgx.ErrNoRows {
		err = tx.QueryRow(ctx, `INSERT INTO orders (customer_id, status) VALUES ($1, 'pending') RETURNING id`, customerID).Scan(&orderID)
	}
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `INSERT INTO order_items (order_id, item_name, quantity) VALUES ($1, $2, 1)`, orderID, item)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (p *PostgresStorage) CompleteOrder(ctx context.Context, customerID string, address string) ([]string, error) {
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var orderID int
	err = tx.QueryRow(ctx, `
		UPDATE orders SET status = 'completed', address = $2 
		WHERE customer_id = $1 AND status = 'pending' 
		RETURNING id`, customerID, address).Scan(&orderID)
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `SELECT item_name FROM order_items WHERE order_id = $1`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			items = append(items, name)
		}
	}

	return items, tx.Commit(ctx)
}

func (p *PostgresStorage) CancelPendingOrder(ctx context.Context, customerID string) error {
	_, err := p.db.Exec(ctx, `
		UPDATE orders 
		SET status = 'cancelled' 
		WHERE customer_id = $1 AND status = 'pending'`, customerID)
	return err
}

func (p *PostgresStorage) GetPendingItems(ctx context.Context, customerID string) ([]string, error) {
	var orderID int
	err := p.db.QueryRow(ctx, `SELECT id FROM orders WHERE customer_id = $1 AND status = 'pending' LIMIT 1`, customerID).Scan(&orderID)
	if err == pgx.ErrNoRows {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := p.db.Query(ctx, `SELECT item_name FROM order_items WHERE order_id = $1`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			items = append(items, name)
		}
	}
	return items, nil
}
