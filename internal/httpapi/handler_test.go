package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/url-shortener/internal/shortener"
	"example.com/url-shortener/internal/storage/memory"
)

func testHandler() http.Handler {
	return New(shortener.New(memory.New(10)), "http://localhost:8080", func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
}
func request(h http.Handler, method, path, body, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestCreateAndResolve(t *testing.T) {
	h := testHandler()
	body := `{"url":"https://example.com/path?x=1&y=2"}`
	first := request(h, "POST", "/api/links", body, "application/json; charset=utf-8")
	if first.Code != 201 {
		t.Fatal(first.Code, first.Body.String())
	}
	var result createResponse
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !shortener.ValidCode(result.Code) || result.ShortURL != "http://localhost:8080/"+result.Code || first.Header().Get("Location") != result.ShortURL {
		t.Fatal(result)
	}
	second := request(h, "POST", "/api/links", body, "application/json")
	if second.Code != 200 || second.Body.String() != first.Body.String() {
		t.Fatal(second.Code, second.Body.String())
	}
	get := request(h, "GET", "/"+result.Code, "", "")
	if get.Code != 200 || get.Header().Get("Location") != "" {
		t.Fatal(get.Code)
	}
	var decoded map[string]string
	if err := json.Unmarshal(get.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["original_url"] != result.OriginalURL {
		t.Fatal(decoded)
	}
}

func TestRequestErrors(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, contentType string
		status                                int
	}{
		{"missing media type", "POST", "/api/links", `{}`, "", 415},
		{"wrong media type", "POST", "/api/links", `{}`, "text/plain", 415},
		{"malformed", "POST", "/api/links", `{`, "application/json", 400},
		{"empty body", "POST", "/api/links", "", "application/json", 400},
		{"unknown field", "POST", "/api/links", `{"url":"https://example.com","extra":true}`, "application/json", 400},
		{"wrong type", "POST", "/api/links", `{"url":12}`, "application/json", 400},
		{"missing URL", "POST", "/api/links", `{}`, "application/json", 400},
		{"null", "POST", "/api/links", `null`, "application/json", 400},
		{"array", "POST", "/api/links", `[]`, "application/json", 400},
		{"two objects", "POST", "/api/links", `{"url":"https://example.com"}{}`, "application/json", 400},
		{"trailing garbage", "POST", "/api/links", `{"url":"https://example.com"}xyz`, "application/json", 400},
		{"invalid URL", "POST", "/api/links", `{"url":"ftp://example.com"}`, "application/json", 400},
		{"too large", "POST", "/api/links", `{"url":"https://example.com/` + strings.Repeat("x", maxBodyBytes) + `"}`, "application/json", 413},
		{"large trailing whitespace", "POST", "/api/links", `{"url":"https://example.com"}` + strings.Repeat(" ", maxBodyBytes), "application/json", 413},
		{"not found", "GET", "/AAAAAAAAAA", "", "", 404},
		{"invalid code", "GET", "/abc-defghi", "", "", 400},
		{"wrong create method", "GET", "/api/links", "", "", 405},
		{"wrong get method", "POST", "/AAAAAAAAAA", "", "", 405},
		{"wrong health method", "POST", "/healthz", "", "", 405},
		{"wrong ready method", "POST", "/readyz", "", "", 405},
		{"unknown route", "GET", "/api/missing", "", "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(testHandler(), tc.method, tc.path, tc.body, tc.contentType)
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
				t.Fatal(w.Header())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] == nil {
				t.Fatal(body, err)
			}
			if tc.status == 405 && w.Header().Get("Allow") == "" {
				t.Fatal("missing Allow")
			}
		})
	}
}

type failingService struct{ err error }

func (f failingService) Create(context.Context, string) (shortener.Link, bool, error) {
	return shortener.Link{}, false, f.err
}
func (f failingService) Resolve(context.Context, string) (shortener.Link, error) {
	return shortener.Link{}, f.err
}

func TestServiceFailures(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{shortener.ErrCapacity, 503}, {shortener.ErrUnavailable, 503}, {context.Canceled, 503}, {context.DeadlineExceeded, 503}, {errors.New("secret-password"), 500},
	} {
		h := New(failingService{tc.err}, "http://localhost:8080", func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
		for _, method := range []string{"GET", "POST"} {
			path, body := "/AAAAAAAAAA", ""
			if method == "POST" {
				path = "/api/links"
				body = `{"url":"https://example.com"}`
			}
			w := request(h, method, path, body, "application/json")
			if w.Code != tc.status || strings.Contains(w.Body.String(), "secret-password") {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
}

func TestHealth(t *testing.T) {
	for _, path := range []string{"/healthz", "/readyz"} {
		if w := request(testHandler(), "GET", path, "", ""); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	h := New(failingService{}, "http://localhost:8080", func(context.Context) error { return errors.New("offline") }, slog.Default())
	if w := request(h, "GET", "/readyz", "", ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", "/healthz", "", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
}
