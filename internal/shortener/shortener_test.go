package shortener

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type fakeStore struct {
	found     Link
	findErr   error
	saveErr   error
	conflicts int
	saves     int
	created   bool
}

func (f *fakeStore) FindByURL(context.Context, string) (Link, error) { return f.found, f.findErr }
func (f *fakeStore) Get(context.Context, string) (Link, error)       { return f.found, f.findErr }
func (f *fakeStore) Save(_ context.Context, l Link) (Link, bool, error) {
	f.saves++
	if f.saves <= f.conflicts {
		return Link{}, false, ErrCodeConflict
	}
	if f.saveErr != nil {
		return Link{}, false, f.saveErr
	}
	if f.found.Code != "" {
		return f.found, false, nil
	}
	return l, f.created, nil
}

func TestCreate(t *testing.T) {
	broken := errors.New("storage offline")
	for _, tc := range []struct {
		name        string
		store       fakeStore
		wantErr     error
		wantSaves   int
		wantCreated bool
	}{
		{"new", fakeStore{findErr: ErrNotFound, created: true}, nil, 1, true},
		{"existing", fakeStore{found: Link{Code: "AAAAAAAAAA", URL: "https://example.com"}}, nil, 0, false},
		{"concurrent winner", fakeStore{findErr: ErrNotFound, found: Link{Code: "AAAAAAAAAA", URL: "https://example.com"}}, nil, 1, false},
		{"collision then success", fakeStore{findErr: ErrNotFound, conflicts: 1, created: true}, nil, 2, true},
		{"retry limit", fakeStore{findErr: ErrNotFound, conflicts: maxAttempts}, ErrUnavailable, maxAttempts, false},
		{"read failure", fakeStore{findErr: broken}, broken, 0, false},
		{"write failure", fakeStore{findErr: ErrNotFound, saveErr: broken}, broken, 1, false},
		{"capacity", fakeStore{findErr: ErrNotFound, saveErr: ErrCapacity}, ErrCapacity, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(&tc.store)
			generated := 0
			s.generate = func() (string, error) { generated++; return fmt.Sprintf("%010d", generated), nil }
			link, created, err := s.Create(context.Background(), "https://example.com")
			if !errors.Is(err, tc.wantErr) || created != tc.wantCreated || tc.store.saves != tc.wantSaves {
				t.Fatalf("link=%+v created=%v err=%v saves=%d", link, created, err, tc.store.saves)
			}
			if err == nil && (link.URL != "https://example.com" || !ValidCode(link.Code)) {
				t.Fatalf("bad link: %+v", link)
			}
			if tc.name == "collision then success" && link.Code != "0000000002" {
				t.Fatal("collision did not generate another code")
			}
		})
	}
}

func TestCreateInvalidCanceledAndGeneratorFailure(t *testing.T) {
	f := &fakeStore{findErr: ErrNotFound}
	s := New(f)
	s.generate = func() (string, error) { return "", io.ErrUnexpectedEOF }
	if _, _, err := s.Create(context.Background(), "bad"); !errors.Is(err, ErrInvalidURL) {
		t.Fatal(err)
	}
	if _, _, err := s.Create(context.Background(), "https://example.com"); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.Create(ctx, "https://example.com"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.saves != 0 {
		t.Fatal("unexpected save")
	}
}

func TestResolve(t *testing.T) {
	f := &fakeStore{found: Link{Code: "Abc123_xyz", URL: "https://example.com"}}
	s := New(f)
	if _, err := s.Resolve(context.Background(), "short"); !errors.Is(err, ErrInvalidCode) {
		t.Fatal(err)
	}
	f.found.Code = "Abc123_xy0"
	if got, err := s.Resolve(context.Background(), f.found.Code); err != nil || got != f.found {
		t.Fatal(got, err)
	}
	f.findErr = ErrNotFound
	if _, err := s.Resolve(context.Background(), f.found.Code); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestValidateURL(t *testing.T) {
	prefix := "https://example.com/"
	for _, raw := range []string{"https://example.com", "http://localhost:8080/x?a=1&b=2#f", "https://example.com/путь", prefix + strings.Repeat("x", MaxURLBytes-len(prefix))} {
		if err := ValidateURL(raw); err != nil {
			t.Errorf("%q: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "example.com", "/relative", "ftp://example.com", "https://", "https://user:password@example.com", "https://example.com/ space", "https://example.com/\x00", "https://example.com/%zz", "https://example.com:abc/", prefix + strings.Repeat("x", MaxURLBytes-len(prefix)+1), prefix + string([]byte{255})} {
		if !errors.Is(ValidateURL(raw), ErrInvalidURL) {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestCodeFormat(t *testing.T) {
	for range 1000 {
		code, err := randomCode()
		if err != nil || !ValidCode(code) {
			t.Fatalf("%q: %v", code, err)
		}
	}
	// Никакого вероятностного теста «все случайные значения уникальны»:
	// совпадение допустимо; корректность обеспечивается обработкой конфликта.
	for _, code := range []string{"", "123456789", "12345678901", "abcdefgh-0", "abcdefgh/0", "абвгд"} {
		if ValidCode(code) {
			t.Errorf("accepted %q", code)
		}
	}
	if !ValidCode("aAZ09_____") {
		t.Fatal("valid alphabet rejected")
	}
}

func TestCodeRejectionSampling(t *testing.T) {
	input := []byte{252, 253, 254, 255, 0, 62, 63, 125, 126, 188, 189, 251, 1, 2, 3, 4}
	got, err := codeFrom(bytes.NewReader(input))
	if err != nil || got != "a_a_a_a_bc" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := codeFrom(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	// Первый блок целиком отброшен, поэтому читается следующий блок.
	input = append(bytes.Repeat([]byte{255}, 16), bytes.Repeat([]byte{0}, 16)...)
	if got, err := codeFrom(bytes.NewReader(input)); err != nil || got != "aaaaaaaaaa" {
		t.Fatal(got, err)
	}
}

func BenchmarkRandomCode(b *testing.B) {
	for b.Loop() {
		if _, err := randomCode(); err != nil {
			b.Fatal(err)
		}
	}
}
