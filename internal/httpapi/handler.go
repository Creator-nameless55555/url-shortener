package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"example.com/url-shortener/internal/shortener"
)

const maxBodyBytes = 16 << 10

type service interface {
	Create(context.Context, string) (shortener.Link, bool, error)
	Resolve(context.Context, string) (shortener.Link, error)
}

type handler struct {
	service service
	baseURL string
	ready   func(context.Context) error
	log     *slog.Logger
}

type createResponse struct {
	Code        string `json:"code"`
	ShortURL    string `json:"short_url"`
	OriginalURL string `json:"original_url"`
}

func New(s service, baseURL string, ready func(context.Context) error, log *slog.Logger) http.Handler {
	h := &handler{service: s, baseURL: baseURL, ready: ready, log: log}
	mux := http.NewServeMux()
	// Метод проверяем сами: ошибки маршрутов и методов тоже возвращаем как JSON.
	mux.HandleFunc("/api/links", h.create)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/readyz", h.readiness)
	mux.HandleFunc("/{code}", h.resolve)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Route not found")
	})
	return mux
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Use Content-Type: application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	defer r.Body.Close()
	var input struct {
		URL string `json:"url"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		h.badJSON(w, err)
		return
	}
	// Без второго чтения {"url":"..."} {"another":"object"} считалось бы допустимым.
	if err := decoder.Decode(new(any)); err != io.EOF {
		h.badJSON(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	link, created, err := h.service.Create(ctx, input.URL)
	if err != nil {
		h.failure(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	shortURL := h.baseURL + "/" + link.Code
	w.Header().Set("Location", shortURL)
	writeJSON(w, status, createResponse{Code: link.Code, ShortURL: shortURL, OriginalURL: link.URL})
}

func (h *handler) resolve(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	link, err := h.service.Resolve(ctx, r.PathValue("code"))
	if err != nil {
		h.failure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"original_url": link.URL})
}

func (h *handler) readiness(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := h.ready(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "Storage is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *handler) badJSON(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "Request body exceeds 16 KiB")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json", "Expected one JSON object with a string field url")
}

func (h *handler) failure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, shortener.ErrInvalidURL):
		writeError(w, 400, "invalid_url", "Use an absolute http/https URL, 1..2048 bytes, without credentials or whitespace")
	case errors.Is(err, shortener.ErrInvalidCode):
		writeError(w, 400, "invalid_code", "Code must contain 10 characters: a-z, A-Z, 0-9 or _")
	case errors.Is(err, shortener.ErrNotFound):
		writeError(w, 404, "not_found", "Link not found")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), errors.Is(err, shortener.ErrCapacity), errors.Is(err, shortener.ErrUnavailable):
		writeError(w, 503, "unavailable", "Service temporarily unavailable")
	default:
		// SQL-ошибка может содержать URL или пароль. Не отправляем и не логируем её текст.
		// SQLSTATE помогает диагностировать БД, не раскрывая пользовательские данные.
		var sqlError interface{ SQLState() string }
		state := ""
		if errors.As(err, &sqlError) {
			state = sqlError.SQLState()
		}
		h.log.Error("request failed", "sqlstate", state)
		writeError(w, 500, "internal_error", "Internal server error")
	}
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use "+method)
	return false
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	// Ответ состоит из простых структур. Ошибка здесь означает, что клиент уже ушёл;
	// заменить начатый ответ новым HTTP-статусом всё равно нельзя.
	_ = json.NewEncoder(w).Encode(body)
}
