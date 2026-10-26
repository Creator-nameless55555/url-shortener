package config

import (
	"errors"
	"flag"
	"io"
	"testing"
)

func TestParse(t *testing.T) {
	env := func(string) string { return "postgres://example" }
	c, err := Parse(nil, env, io.Discard)
	if err != nil || c.Storage != "memory" || c.MemoryLimit != 100000 || c.Addr != ":8080" {
		t.Fatal(c, err)
	}
	c, err = Parse([]string{"-storage=postgres", "-base-url=https://s.example/", "-addr=127.0.0.1:9000", "-migrate"}, env, io.Discard)
	if err != nil || c.BaseURL != "https://s.example" || !c.Migrate {
		t.Fatal(c, err)
	}
	for _, args := range [][]string{
		{"-storage=redis"}, {"-memory-limit=0"}, {"-addr=oops"}, {"-base-url=ftp://example.com"}, {"-base-url=https://example.com/path"}, {"-base-url=https://example.com/?x=1"}, {"-base-url=https://example.com?"}, {"-base-url=https://example.com/#fragment"}, {"-base-url=https://u:p@example.com"}, {"-migrate"}, {"extra"}, {"-unknown"},
	} {
		if _, err := Parse(args, env, io.Discard); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	if _, err := Parse([]string{"-storage=postgres"}, func(string) string { return "" }, io.Discard); err == nil {
		t.Fatal("missing DSN accepted")
	}
	if _, err := Parse([]string{"-h"}, env, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
}
