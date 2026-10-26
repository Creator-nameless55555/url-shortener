package config

import (
	"errors"
	"flag"
	"io"
	"net"
	"net/url"
	"strings"
)

type Config struct {
	Storage     string
	Addr        string
	BaseURL     string
	DatabaseURL string
	MemoryLimit int
	Migrate     bool
}

func Parse(args []string, getenv func(string) string, output io.Writer) (Config, error) {
	var c Config
	flags := flag.NewFlagSet("shortener", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&c.Storage, "storage", "memory", "storage: memory or postgres")
	flags.StringVar(&c.Addr, "addr", ":8080", "HTTP listen address")
	flags.StringVar(&c.BaseURL, "base-url", "http://localhost:8080", "public URL origin, without a path")
	flags.IntVar(&c.MemoryLimit, "memory-limit", 100000, "maximum number of in-memory links")
	flags.BoolVar(&c.Migrate, "migrate", false, "create PostgreSQL schema and exit")
	if err := flags.Parse(args); err != nil {
		return c, err
	}
	if flags.NArg() != 0 {
		return c, errors.New("unexpected positional arguments")
	}
	if c.Storage != "memory" && c.Storage != "postgres" {
		return c, errors.New("storage must be memory or postgres")
	}
	if c.MemoryLimit <= 0 {
		return c, errors.New("memory-limit must be positive")
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return c, errors.New("addr must be host:port or :port")
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(c.BaseURL, " \t\r\n#") {
		return c, errors.New("base-url must be an http/https origin without credentials, path, query or fragment")
	}
	c.DatabaseURL = getenv("DATABASE_URL")
	if c.Storage == "postgres" && c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required for postgres")
	}
	if c.Migrate && c.Storage != "postgres" {
		return c, errors.New("migrate requires storage=postgres")
	}
	return c, nil
}
