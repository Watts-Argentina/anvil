package completion

import (
	"reflect"
	"slices"
	"testing"

	"github.com/Watts-Argentina/anvil/internal/drivers"
	"github.com/Watts-Argentina/anvil/models"
)

func TestWordAt(t *testing.T) {
	tests := []struct {
		line string
		col  int
		want Word
	}{
		{"SELECT na", 9, Word{Start: 7, Prefix: "na"}},
		{"SELECT ", 7, Word{Start: 7}},
		{"where u.na", 10, Word{Start: 8, Prefix: "na", Qualifier: "u"}},
		{"where u.", 8, Word{Start: 8, Qualifier: "u"}},
		{"x público.añ", len("x público.añ"), Word{Start: len("x público."), Prefix: "añ", Qualifier: "público"}},
		{"abc", 99, Word{Start: 0, Prefix: "abc"}},
	}
	for _, tt := range tests {
		if got := WordAt(tt.line, tt.col); got != tt.want {
			t.Errorf("WordAt(%q, %d) = %+v, want %+v", tt.line, tt.col, got, tt.want)
		}
	}
}

func texts(cs []Candidate) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Text)
	}
	return out
}

func TestComplete(t *testing.T) {
	src := Source{
		Keywords: Keywords(models.EngineSQLite),
		Tables:   []drivers.Table{{Schema: "app", Name: "users"}, {Schema: "app", Name: "orders"}},
		Columns: map[string][]drivers.Column{
			"users":  {{Name: "id"}, {Name: "user_name"}},
			"orders": {{Name: "id"}, {Name: "user_id"}, {Name: "ordered_at"}},
		},
	}
	tests := []struct {
		name string
		w    Word
		want []string
	}{
		{"columns then tables then keywords", Word{Prefix: "u"}, []string{"user_id", "user_name", "users", "using", "union", "update", "unique"}},
		{"keyword keeps upper case", Word{Prefix: "SEL"}, []string{"SELECT"}},
		{"ignores case of identifiers", Word{Prefix: "ORD"}, []string{"ordered_at", "orders", "ORDER"}},
		{"skips the word itself", Word{Prefix: "from"}, []string{}},
		{"empty prefix suggests nothing", Word{}, []string{}},
		{"table qualifier", Word{Qualifier: "Users"}, []string{"id", "user_name"}},
		{"schema qualifier", Word{Qualifier: "app", Prefix: "o"}, []string{"orders"}},
		{"alias qualifier", Word{Qualifier: "o", Prefix: "user"}, []string{"user_id", "user_name"}},
	}
	for _, tt := range tests {
		if got := texts(Complete(tt.w, src)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCompleteLimit(t *testing.T) {
	cols := make([]drivers.Column, Limit+10)
	for i := range cols {
		cols[i].Name = "c" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if got := len(Complete(Word{Prefix: "c"}, Source{Columns: map[string][]drivers.Column{"t": cols}})); got != Limit {
		t.Errorf("got %d candidates, want %d", got, Limit)
	}
}

func TestMentioned(t *testing.T) {
	tables := []drivers.Table{{Name: "users"}, {Name: "orders"}, {Name: "user"}}
	got := Mentioned("SELECT * FROM Users u JOIN app.orders o", tables)
	want := []drivers.Table{{Name: "users"}, {Name: "orders"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestKeywordsDoNotShareBacking(t *testing.T) {
	pg := Keywords(models.EnginePostgres)
	Keywords(models.EngineMySQL)
	if !slices.Contains(pg, "ILIKE") {
		t.Fatal("Keywords(mysql) overwrote the postgres keywords")
	}
}
