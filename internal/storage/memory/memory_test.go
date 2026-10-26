package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"example.com/url-shortener/internal/shortener"
)

func TestStore(t *testing.T) {
	ctx := context.Background()
	s := New(1)
	first := shortener.Link{Code: "AAAAAAAAAA", URL: "https://example.com"}
	if _, err := s.Get(ctx, first.Code); !errors.Is(err, shortener.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.FindByURL(ctx, first.URL); !errors.Is(err, shortener.ErrNotFound) {
		t.Fatal(err)
	}
	if got, created, err := s.Save(ctx, first); got != first || !created || err != nil {
		t.Fatal(got, created, err)
	}
	if got, err := s.Get(ctx, first.Code); got != first || err != nil {
		t.Fatal(got, err)
	}
	if got, err := s.FindByURL(ctx, first.URL); got != first || err != nil {
		t.Fatal(got, err)
	}
	// Уже сохранённый URL доступен даже после заполнения хранилища.
	if got, created, err := s.Save(ctx, shortener.Link{Code: "BBBBBBBBBB", URL: first.URL}); got != first || created || err != nil {
		t.Fatal(got, created, err)
	}
	if _, _, err := s.Save(ctx, shortener.Link{Code: first.Code, URL: "https://other.com"}); !errors.Is(err, shortener.ErrCodeConflict) {
		t.Fatal(err)
	}
	if _, _, err := s.Save(ctx, shortener.Link{Code: "BBBBBBBBBB", URL: "https://other.com"}); !errors.Is(err, shortener.ErrCapacity) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Get(canceled, first.Code); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.FindByURL(canceled, first.URL); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, err := s.Save(canceled, first); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestConcurrentSameURL(t *testing.T) {
	const count = 300
	s := New(count)
	ctx := context.Background()
	results := make(chan shortener.Link, count)
	var createdCount atomic.Int32
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			l, created, err := s.Save(ctx, shortener.Link{Code: fmt.Sprintf("%010d", i), URL: "https://example.com"})
			if err != nil {
				t.Error(err)
				return
			}
			if created {
				createdCount.Add(1)
			}
			results <- l
		})
	}
	wg.Wait()
	close(results)
	var first string
	for l := range results {
		if first == "" {
			first = l.Code
		}
		if l.Code != first {
			t.Error("different codes for the same URL")
		}
	}
	if createdCount.Load() != 1 || len(s.byCode) != 1 || len(s.byURL) != 1 {
		t.Fatal("expected exactly one record")
	}
}

func TestConcurrentDifferentURLsAndReads(t *testing.T) {
	const count = 300
	s := New(count)
	service := shortener.New(s)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			original := fmt.Sprintf("https://example.com/%d", i)
			l, created, err := service.Create(ctx, original)
			if err != nil || !created {
				t.Error(l, created, err)
				return
			}
			if got, err := service.Resolve(ctx, l.Code); err != nil || got != l {
				t.Error(got, err)
			}
		})
	}
	wg.Wait()
	if len(s.byCode) != count {
		t.Fatal(len(s.byCode))
	}
}

func TestConcurrentSameCode(t *testing.T) {
	s := New(100)
	var wg sync.WaitGroup
	var successes, conflicts atomic.Int32
	for i := range 100 {
		wg.Go(func() {
			_, _, err := s.Save(context.Background(), shortener.Link{Code: "AAAAAAAAAA", URL: fmt.Sprintf("https://example.com/%d", i)})
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
}
