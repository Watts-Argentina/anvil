package export

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Watts-Argentina/anvil/internal/query"
)

var sample = query.Result{
	Columns: []query.Column{{Name: "id"}, {Name: "name"}, {Name: "data"}, {Name: "at"}, {Name: "ok"}, {Name: "ratio"}},
	Rows: [][]query.Value{
		{
			{Text: "1", Raw: int64(1)},
			{Text: `say "hi", then` + "\nbye", Raw: `say "hi", then` + "\nbye"},
			{Blob: true, Text: "<BLOB 3 B> 00ff10", Raw: []byte{0x00, 0xff, 0x10}},
			{Text: "2024-03-01 10:20:30 +00:00", Raw: time.Date(2024, 3, 1, 10, 20, 30, 0, time.UTC)},
			{Text: "true", Raw: true},
			{Text: "NaN", Raw: math.NaN()},
		},
		{
			{Text: "2", Raw: int64(2)},
			{Text: "", Raw: ""},
			{Null: true},
			{Null: true},
			{Null: true},
			{Text: "0.5", Raw: 0.5},
		},
	},
}

func write(t *testing.T, f Format, res query.Result) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out."+string(f))
	n, err := ToFile(path, f, Result(res))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(res.Rows) {
		t.Errorf("rows = %d, want %d", n, len(res.Rows))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCSV(t *testing.T) {
	want := "id,name,data,at,ok,ratio\n" +
		"1,\"say \"\"hi\"\", then\nbye\",AP8Q,2024-03-01T10:20:30Z,true,NaN\n" +
		"2,,,,,0.5\n"
	if got := write(t, CSV, sample); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestJSON(t *testing.T) {
	want := `[
{"id":1,"name":"say \"hi\", then\nbye","data":"AP8Q","at":"2024-03-01T10:20:30Z","ok":true,"ratio":"NaN"},
{"id":2,"name":"","data":null,"at":null,"ok":null,"ratio":0.5}
]
`
	got := write(t, JSON, sample)
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	var v []map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatal(err)
	}
}

func TestEmpty(t *testing.T) {
	cols := query.Result{Columns: sample.Columns}
	if got := write(t, JSON, cols); got != "[\n]\n" {
		t.Errorf("json = %q", got)
	}
	if got := write(t, CSV, cols); got != "id,name,data,at,ok,ratio\n" {
		t.Errorf("csv = %q", got)
	}
}

func TestFailureLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.csv")
	os.WriteFile(path, []byte("old"), 0o644)
	boom := errors.New("connection lost")
	_, err := ToFile(path, CSV, func(w *Writer) error {
		Result(sample)(w)
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if b, _ := os.ReadFile(path); len(entries) != 1 || string(b) != "old" {
		t.Errorf("dir has %d entries, file = %q", len(entries), b)
	}
}
