// Package postgres реализует тот же контракт хранилища через PostgreSQL.
package postgres

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"example.com/url-shortener/internal/shortener"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// Маленький интерфейс позволяет проверить обработку ошибок без настоящей БД.
type database interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Store struct{ db database }

func New(pool *pgxpool.Pool) *Store { return &Store{db: pool} }

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// Миграция запускается отдельной командой до старта HTTP-сервиса.
	_, err := pool.Exec(ctx, schema)
	if err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	return nil
}

func (s *Store) FindByURL(ctx context.Context, original string) (shortener.Link, error) {
	return scanLink(s.db.QueryRow(ctx, `SELECT code, original_url FROM links WHERE original_url = $1`, original))
}

func (s *Store) Get(ctx context.Context, code string) (shortener.Link, error) {
	return scanLink(s.db.QueryRow(ctx, `SELECT code, original_url FROM links WHERE code = $1`, code))
}

func (s *Store) Save(ctx context.Context, link shortener.Link) (shortener.Link, bool, error) {
	saved, err := scanLink(s.db.QueryRow(ctx, `
  INSERT INTO links (code, original_url) VALUES ($1, $2)
  ON CONFLICT DO NOTHING
  RETURNING code, original_url`, link.Code, link.URL))
	if err == nil {
		return saved, true, nil
	}
	if !errors.Is(err, shortener.ErrNotFound) {
		return shortener.Link{}, false, err
	}

	// Конфликт мог быть по URL или по коду. Отдельный SELECT видит запись
	// конкурента после его COMMIT. Один CTE с INSERT и SELECT этого не гарантирует:
	// у частей одного SQL-запроса общий снимок данных.
	existing, err := s.FindByURL(ctx, link.URL)
	if err == nil {
		return existing, false, nil
	}
	if errors.Is(err, shortener.ErrNotFound) {
		return shortener.Link{}, false, shortener.ErrCodeConflict
	}
	return shortener.Link{}, false, err
}

func scanLink(row pgx.Row) (shortener.Link, error) {
	var link shortener.Link
	err := row.Scan(&link.Code, &link.URL)
	if errors.Is(err, pgx.ErrNoRows) {
		return shortener.Link{}, shortener.ErrNotFound
	}
	if err != nil {
		return shortener.Link{}, fmt.Errorf("query link: %w", err)
	}
	return link, nil
}
