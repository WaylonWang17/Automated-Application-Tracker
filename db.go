package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func initDB(ctx context.Context, connStr string) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	_, err = conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS users (
			email           TEXT PRIMARY KEY,
			last_scanned_at TIMESTAMPTZ,
			since_date      TEXT
		);
		CREATE TABLE IF NOT EXISTS applications (
			id          SERIAL PRIMARY KEY,
			user_email  TEXT NOT NULL REFERENCES users(email) ON DELETE CASCADE,
			subject     TEXT,
			sender      TEXT,
			date_header TEXT,
			snippet     TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_applications_user ON applications(user_email);
	`)
	if err != nil {
		conn.Close(ctx)
		return nil, fmt.Errorf("create tables: %w", err)
	}

	return conn, nil
}

func upsertUser(ctx context.Context, conn *pgx.Conn, email, sinceDate string) error {
	_, err := conn.Exec(ctx, `
		INSERT INTO users (email, last_scanned_at, since_date)
		VALUES ($1, $2, $3)
		ON CONFLICT (email) DO UPDATE
		SET last_scanned_at = EXCLUDED.last_scanned_at,
		    since_date = EXCLUDED.since_date
	`, email, time.Now().UTC(), sinceDate)
	return err
}

// replaceApplications deletes all existing rows for the user and inserts fresh ones.
func replaceApplications(ctx context.Context, conn *pgx.Conn, email string, apps []Application) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `DELETE FROM applications WHERE user_email = $1`, email)
	if err != nil {
		return err
	}

	for _, app := range apps {
		_, err = tx.Exec(ctx, `
			INSERT INTO applications (user_email, subject, sender, date_header, snippet)
			VALUES ($1, $2, $3, $4, $5)
		`, email, app.Subject, app.From, app.Date, app.Snippet)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func getApplicationsFromDB(ctx context.Context, conn *pgx.Conn, email string) ([]Application, error) {
	rows, err := conn.Query(ctx, `
		SELECT subject, sender, date_header, snippet
		FROM applications
		WHERE user_email = $1
		ORDER BY id DESC
	`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Application
	for rows.Next() {
		var app Application
		if err := rows.Scan(&app.Subject, &app.From, &app.Date, &app.Snippet); err != nil {
			return nil, err
		}
		results = append(results, app)
	}
	return results, rows.Err()
}

type UserInfo struct {
	Email       string `json:"email"`
	LastScanned string `json:"lastScanned"`
	SinceDate   string `json:"sinceDate"`
	Count       int    `json:"count"`
}

func getUserInfo(ctx context.Context, conn *pgx.Conn, email string) (*UserInfo, error) {
	var info UserInfo
	info.Email = email

	err := conn.QueryRow(ctx, `
		SELECT COALESCE(to_char(last_scanned_at, 'Mon DD, YYYY'), ''),
		       COALESCE(since_date, '')
		FROM users WHERE email = $1
	`, email).Scan(&info.LastScanned, &info.SinceDate)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	err = conn.QueryRow(ctx, `SELECT COUNT(*) FROM applications WHERE user_email = $1`, email).Scan(&info.Count)
	if err != nil {
		return nil, err
	}

	return &info, nil
}
