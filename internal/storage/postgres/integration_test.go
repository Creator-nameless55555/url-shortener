//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/url-shortener/internal/shortener"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("integration tests require TEST_DATABASE_URL pointing to a test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	// Своя схема на каждый запуск: тесты не очищают таблицы приложения.
	schemaName := fmt.Sprintf("test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schemaName+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	newPool := func() *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schemaName
		cfg.MaxConns = 10
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	p1, p2 := newPool(), newPool()
	if err := Migrate(ctx, p1); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, p1); err != nil {
		t.Fatalf("migration is not repeatable: %v", err)
	}
	stores := []*Store{New(p1), New(p2)}
	first := shortener.Link{Code: "AAAAAAAAAA", URL: "https://example.com"}
	if got, created, err := stores[0].Save(ctx, first); err != nil || !created || got != first {
		t.Fatal(got, created, err)
	}
	if got, err := stores[1].Get(ctx, first.Code); err != nil || got != first {
		t.Fatal(got, err)
	}
	if got, created, err := stores[1].Save(ctx, shortener.Link{Code: "BBBBBBBBBB", URL: first.URL}); err != nil || created || got != first {
		t.Fatal(got, created, err)
	}
	if _, _, err := stores[1].Save(ctx, shortener.Link{Code: first.Code, URL: "https://other.com"}); !errors.Is(err, shortener.ErrCodeConflict) {
		t.Fatal(err)
	}
	if _, err := stores[1].Get(ctx, "ZZZZZZZZZZ"); !errors.Is(err, shortener.ErrNotFound) {
		t.Fatal(err)
	}

	t.Run("300 concurrent requests for one URL across two pools", func(t *testing.T) {
		const count = 300
		var wg sync.WaitGroup
		var createdCount atomic.Int32
		results := make(chan string, count)
		for i := range count {
			wg.Go(func() {
				l, created, err := stores[i%2].Save(ctx, shortener.Link{Code: fmt.Sprintf("%010d", i), URL: "https://concurrent.example"})
				if err != nil {
					t.Error(err)
					return
				}
				if created {
					createdCount.Add(1)
				}
				results <- l.Code
			})
		}
		wg.Wait()
		close(results)
		var code string
		received := 0
		for c := range results {
			received++
			if code == "" {
				code = c
			}
			if code != c {
				t.Error("different codes for one URL")
			}
		}
		if createdCount.Load() != 1 || received != count {
			t.Fatal(createdCount.Load(), received)
		}
	})
	t.Run("same code different URLs", func(t *testing.T) {
		var wg sync.WaitGroup
		var successes, conflicts atomic.Int32
		for i := range 100 {
			wg.Go(func() {
				_, _, err := stores[i%2].Save(ctx, shortener.Link{Code: "CCCCCCCCCC", URL: fmt.Sprintf("https://collision.example/%d", i)})
				if err == nil {
					successes.Add(1)
				} else if errors.Is(err, shortener.ErrCodeConflict) {
					conflicts.Add(1)
				} else {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if successes.Load() != 1 || conflicts.Load() != 99 {
			t.Fatal(successes.Load(), conflicts.Load())
		}
	})
	t.Run("boundary and SQL parameters", func(t *testing.T) {
		raw := "https://example.com/" + strings.Repeat("x", 2048-len("https://example.com/"))
		l := shortener.Link{Code: "LLLLLLLLLL", URL: raw}
		if _, _, err := stores[0].Save(ctx, l); err != nil {
			t.Fatal(err)
		}
		l = shortener.Link{Code: "SSSSSSSSSS", URL: "https://example.com/';DROP%20TABLE%20links;--"}
		if _, _, err := stores[0].Save(ctx, l); err != nil {
			t.Fatal(err)
		}
		if got, err := stores[1].Get(ctx, l.Code); err != nil || got != l {
			t.Fatal(got, err)
		}
		for _, bad := range []shortener.Link{{Code: "invalid", URL: "https://invalid.example"}, {Code: "TTTTTTTTTT", URL: raw + "x"}} {
			if _, _, err := stores[0].Save(ctx, bad); err == nil {
				t.Fatal("database accepted invalid row")
			}
		}
	})
	// Новое подключение видит данные, сохранённые через старое.
	p3 := newPool()
	if got, err := New(p3).Get(ctx, first.Code); err != nil || got != first {
		t.Fatal(got, err)
	}
}
