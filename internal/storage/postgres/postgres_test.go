package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"example.com/url-shortener/internal/shortener"
	"github.com/jackc/pgx/v5"
)

type fakeRow struct {
	link shortener.Link
	err  error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*string) = r.link.Code
	*dest[1].(*string) = r.link.URL
	return nil
}

type expectedQuery struct {
	part string
	args []any
	row  fakeRow
}
type fakeDB struct {
	t       *testing.T
	queries []expectedQuery
	calls   int
}

func (f *fakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.t.Helper()
	if f.calls >= len(f.queries) {
		f.t.Fatal("unexpected query")
	}
	q := f.queries[f.calls]
	f.calls++
	if !strings.Contains(sql, q.part) || len(args) != len(q.args) {
		f.t.Fatalf("query=%s args=%v", sql, args)
	}
	for i := range args {
		if args[i] != q.args[i] {
			f.t.Fatalf("args: %v", args)
		}
	}
	return q.row
}

func TestSave(t *testing.T) {
	link := shortener.Link{Code: "AAAAAAAAAA", URL: "https://example.com"}
	existing := shortener.Link{Code: "BBBBBBBBBB", URL: link.URL}
	broken := errors.New("connection lost")
	for _, tc := range []struct {
		name    string
		rows    []fakeRow
		want    shortener.Link
		created bool
		err     error
	}{
		{"insert", []fakeRow{{link: link}}, link, true, nil},
		{"URL conflict", []fakeRow{{err: pgx.ErrNoRows}, {link: existing}}, existing, false, nil},
		{"code conflict", []fakeRow{{err: pgx.ErrNoRows}, {err: pgx.ErrNoRows}}, shortener.Link{}, false, shortener.ErrCodeConflict},
		{"insert error", []fakeRow{{err: broken}}, shortener.Link{}, false, broken},
		{"select error", []fakeRow{{err: pgx.ErrNoRows}, {err: broken}}, shortener.Link{}, false, broken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeDB{t: t, queries: []expectedQuery{{part: "ON CONFLICT DO NOTHING", args: []any{link.Code, link.URL}, row: tc.rows[0]}}}
			if len(tc.rows) == 2 {
				db.queries = append(db.queries, expectedQuery{part: "WHERE original_url = $1", args: []any{link.URL}, row: tc.rows[1]})
			}
			s := &Store{db: db}
			got, created, err := s.Save(context.Background(), link)
			if got != tc.want || created != tc.created || !errors.Is(err, tc.err) {
				t.Fatal(got, created, err)
			}
			if db.calls != len(db.queries) {
				t.Fatal("missing query")
			}
		})
	}
}

func TestGet(t *testing.T) {
	link := shortener.Link{Code: "AAAAAAAAAA", URL: "https://example.com"}
	db := &fakeDB{t: t, queries: []expectedQuery{{part: "WHERE code = $1", args: []any{link.Code}, row: fakeRow{link: link}}}}
	if got, err := (&Store{db: db}).Get(context.Background(), link.Code); err != nil || got != link {
		t.Fatal(got, err)
	}
}
