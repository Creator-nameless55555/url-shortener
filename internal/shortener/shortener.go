// Package shortener содержит правила сервиса, не зависящие от HTTP и PostgreSQL.
package shortener

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	CodeLength = 10
	Alphabet   = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"
	// Ограничение в байтах позволяет безопасно индексировать URL целиком в PostgreSQL.
	MaxURLBytes = 2048
	maxAttempts = 8
)

var (
	ErrInvalidURL   = errors.New("invalid URL")
	ErrInvalidCode  = errors.New("invalid code")
	ErrNotFound     = errors.New("link not found")
	ErrCodeConflict = errors.New("code already used")
	ErrCapacity     = errors.New("memory storage is full")
	ErrUnavailable  = errors.New("could not allocate a code")
)

type Link struct {
	Code string
	URL  string
}

// Store должен атомарно сохранять пару. Если URL уже есть, Save возвращает
// прежнюю пару и created=false. Чужой занятый код — ErrCodeConflict.
// Удаление и изменение ссылок намеренно не входят в контракт.
type Store interface {
	FindByURL(context.Context, string) (Link, error)
	Get(context.Context, string) (Link, error)
	Save(context.Context, Link) (link Link, created bool, err error)
}

type Service struct {
	store    Store
	generate func() (string, error)
}

func New(store Store) *Service { return &Service{store: store, generate: randomCode} }

func (s *Service) Create(ctx context.Context, original string) (Link, bool, error) {
	if err := ValidateURL(original); err != nil {
		return Link{}, false, err
	}
	// Частый повторный запрос обходится одним чтением, без генерации и записи.
	link, err := s.store.FindByURL(ctx, original)
	if err == nil {
		return link, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Link{}, false, fmt.Errorf("find URL: %w", err)
	}
	for range maxAttempts {
		if err := ctx.Err(); err != nil {
			return Link{}, false, err
		}
		code, err := s.generate()
		if err != nil {
			return Link{}, false, fmt.Errorf("generate code: %w", err)
		}
		link, created, err := s.store.Save(ctx, Link{Code: code, URL: original})
		if errors.Is(err, ErrCodeConflict) {
			continue
		}
		if err != nil {
			return Link{}, false, fmt.Errorf("save link: %w", err)
		}
		return link, created, nil
	}
	// Ограничиваем повторы: неисправность не должна превращаться в вечный цикл.
	return Link{}, false, ErrUnavailable
}

func (s *Service) Resolve(ctx context.Context, code string) (Link, error) {
	if !ValidCode(code) {
		return Link{}, ErrInvalidCode
	}
	link, err := s.store.Get(ctx, code)
	if err != nil {
		return Link{}, fmt.Errorf("get link: %w", err)
	}
	return link, nil
}

func ValidCode(code string) bool {
	if len(code) != CodeLength {
		return false
	}
	for i := range len(code) {
		if !strings.ContainsRune(Alphabet, rune(code[i])) {
			return false
		}
	}
	return true
}

func ValidateURL(raw string) error {
	if len(raw) == 0 || len(raw) > MaxURLBytes || !utf8.ValidString(raw) {
		return ErrInvalidURL
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return ErrInvalidURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ErrInvalidURL
	}
	// Не нормализуем URL: порядок query-параметров и регистр пути могут быть значимыми.
	return nil
}
