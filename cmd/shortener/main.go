package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/url-shortener/internal/config"
	"example.com/url-shortener/internal/httpapi"
	"example.com/url-shortener/internal/shortener"
	"example.com/url-shortener/internal/storage/memory"
	"example.com/url-shortener/internal/storage/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		// Здесь только безопасные сообщения: строка подключения никогда не попадает в лог.
		log.Error("service stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Parse(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var store shortener.Store
	ready := func(context.Context) error { return nil }
	if cfg.Storage == "memory" {
		store = memory.New(cfg.MemoryLimit)
	} else {
		pc, err := pgxpool.ParseConfig(cfg.DatabaseURL)
		if err != nil {
			return errors.New("invalid DATABASE_URL")
		}
		pc.MaxConns = 10
		pc.ConnConfig.ConnectTimeout = 5 * time.Second
		startup, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		pool, err := pgxpool.NewWithConfig(startup, pc)
		if err != nil {
			return errors.New("cannot initialize database pool")
		}
		defer pool.Close()
		if err := pool.Ping(startup); err != nil {
			return errors.New("cannot connect to PostgreSQL; check DATABASE_URL and server availability")
		}
		if cfg.Migrate {
			if err := postgres.Migrate(startup, pool); err != nil {
				return errors.New("schema migration failed; check database permissions and schema")
			}
			log.Info("schema is ready")
			return nil
		}
		// Fail fast: без схемы приложение не должно выглядеть готовым.
		if _, err := pool.Exec(startup, "SELECT code, original_url FROM links LIMIT 0"); err != nil {
			return errors.New("links table is unavailable; run with -storage=postgres -migrate first")
		}
		store = postgres.New(pool)
		ready = pool.Ping
	}
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.New(shortener.New(store), cfg.BaseURL, ready, log),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	log.Info("HTTP server started", "addr", cfg.Addr, "storage", cfg.Storage)
	select {
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		log.Info("shutdown started")
	}
	// Даём начатым запросам завершиться, затем закрываем пул через defer.
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
