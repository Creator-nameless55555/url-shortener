// Package memory хранит ссылки только до остановки процесса.
package memory

import (
	"context"
	"sync"

	"example.com/url-shortener/internal/shortener"
)

type Store struct {
	mu     sync.RWMutex
	byCode map[string]string
	byURL  map[string]string
	limit  int
}

func New(limit int) *Store {
	return &Store{byCode: make(map[string]string), byURL: make(map[string]string), limit: limit}
}

func (s *Store) FindByURL(ctx context.Context, original string) (shortener.Link, error) {
	if err := ctx.Err(); err != nil {
		return shortener.Link{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	code, ok := s.byURL[original]
	if !ok {
		return shortener.Link{}, shortener.ErrNotFound
	}
	return shortener.Link{Code: code, URL: original}, nil
}

func (s *Store) Get(ctx context.Context, code string) (shortener.Link, error) {
	if err := ctx.Err(); err != nil {
		return shortener.Link{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	original, ok := s.byCode[code]
	if !ok {
		return shortener.Link{}, shortener.ErrNotFound
	}
	return shortener.Link{Code: code, URL: original}, nil
}

func (s *Store) Save(ctx context.Context, link shortener.Link) (shortener.Link, bool, error) {
	if err := ctx.Err(); err != nil {
		return shortener.Link{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Проверка и запись идут под одной блокировкой: два запроса не смогут
	// одновременно решить, что URL свободен, и сохранить разные коды.
	if code, ok := s.byURL[link.URL]; ok {
		return shortener.Link{Code: code, URL: link.URL}, false, nil
	}
	if _, ok := s.byCode[link.Code]; ok {
		return shortener.Link{}, false, shortener.ErrCodeConflict
	}
	if len(s.byCode) >= s.limit {
		return shortener.Link{}, false, shortener.ErrCapacity
	}
	s.byCode[link.Code] = link.URL
	s.byURL[link.URL] = link.Code
	return link, true, nil
}
