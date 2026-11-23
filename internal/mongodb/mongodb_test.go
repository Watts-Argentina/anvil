package mongodb

import (
	"strings"
	"testing"
	"time"

	"github.com/Watts-Argentina/anvil/internal/editing"
	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/models"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func rawDoc(t *testing.T, ext string) bson.Raw {
	t.Helper()
	var d bson.Raw
	if err := bson.UnmarshalExtJSON([]byte(ext), false, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// Every edited value goes back with the type it was read with.
func TestRoundTrip(t *testing.T) {
	doc := rawDoc(t, `{"s": "x", "i": {"$numberInt": "7"}, "l": {"$numberLong": "9"}, "d": 1.5,
		"dec": {"$numberDecimal": "1.10"}, "b": true, "o": {"$oid": "65a1b2c3d4e5f60718293a4b"},
		"t": {"$date": "2024-05-06T07:08:09.123Z"}, "obj": {"a": [1, 2]}, "arr": ["x", {"y": 1}]}`)
	elems, _ := doc.Elements()
	for _, e := range elems {
		orig := e.Value()
		cell := convert(orig)
		back, err := parse(cellType(cell, ""), editing.Text(cell.Text))
		if err != nil {
			t.Fatalf("%s: parse(%q): %v", e.Key(), cell.Text, err)
		}
		_, data, err := bson.MarshalValue(back)
		if err != nil {
			t.Fatal(err)
		}
		got := bson.RawValue{Type: orig.Type, Value: data}
		if !got.Equal(orig) {
			t.Errorf("%s: %q came back as %s, want %s", e.Key(), cell.Text, got, orig)
		}
	}
}

func TestConvert(t *testing.T) {
	doc := rawDoc(t, `{"t": {"$date": "2024-05-06T07:08:09Z"}, "n": null, "bin": {"$binary": {"base64": "AAEC", "subType": "00"}}, "obj": {"a": 1}}`)
	cases := map[string]string{"t": "2024-05-06 07:08:09 +00:00", "bin": "<BINARY 3 B>", "obj": `{"a":1}`}
	for k, want := range cases {
		if got := convert(doc.Lookup(k)).Text; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !convert(doc.Lookup("n")).Null || !convert(doc.Lookup("bin")).Blob {
		t.Error("null must be NULL and binary read-only")
	}
}

func TestParse(t *testing.T) {
	if v, err := parse("int", editing.Text("x")); err == nil {
		t.Errorf("int x = %v, want an error", v)
	}
	if v, _ := parse("datetimetz", editing.Text("2024-01-02 03:04:05")); v.(bson.DateTime).Time().UTC() != time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) {
		t.Errorf("datetime = %v", v)
	}
	// Set Now and the calendar write local time with its offset.
	local := time.Date(2024, 1, 2, 3, 4, 5, 0, time.FixedZone("x", -3*3600))
	if v, _ := parse("datetimetz", editing.Text(editing.FormatTemporal("datetimetz", local))); !v.(bson.DateTime).Time().Equal(local) {
		t.Errorf("local time = %v, want %v", v.(bson.DateTime).Time().UTC(), local.UTC())
	}
	// Unknown type: JSON if it parses, text otherwise.
	if v, _ := parse("", editing.Text("12")); v.(bson.RawValue).Type != bson.TypeInt32 {
		t.Errorf("12 = %v, want an int", v)
	}
	if v, _ := parse("", editing.Text("hello")); v != "hello" {
		t.Errorf("hello = %v", v)
	}
	if v, _ := parse("string", editing.Null); v != nil {
		t.Errorf("NULL = %v", v)
	}
}

func TestFields(t *testing.T) {
	cols := fields([]bson.Raw{
		rawDoc(t, `{"name": "a", "_id": 1, "age": 3}`),
		rawDoc(t, `{"_id": 2, "age": "x", "tags": []}`),
		rawDoc(t, `{"_id": 3, "age": 5}`),
	})
	var got []string
	for _, c := range cols {
		got = append(got, c.Name+":"+c.DBType)
	}
	if s := strings.Join(got, " "); s != "_id:int name:string age:int tags:array" {
		t.Errorf("fields = %s", s)
	}
	if !cols[0].PK || cols[0].Nullable || !cols[1].Nullable {
		t.Error("_id must be the non-null PK")
	}
	if c := fields(nil); len(c) != 1 || c[0].DBType != "objectId" {
		t.Errorf("empty collection = %v", c)
	}
}

func TestNestedFields(t *testing.T) {
	doc := rawDoc(t, `{"addr": {"city": "Rosario", "zip": {"$numberInt": "2000"}}, "tags": ["a", {"b": 1}], "n": null}`)
	var got []string
	for _, f := range Fields(convert(doc.Lookup("addr"))) {
		got = append(got, f.Key+"="+f.Value.Text+":"+f.Type)
	}
	for _, f := range Fields(convert(doc.Lookup("tags"))) {
		got = append(got, f.Key+"="+f.Value.Text+":"+f.Type)
	}
	if s := strings.Join(got, " "); s != `city=Rosario:string zip=2000:int [0]=a:string [1]={"b":1}:object` {
		t.Errorf("fields = %s", s)
	}
	if Fields(convert(doc.Lookup("n"))) != nil || Missing(convert(doc.Lookup("n"))) {
		t.Error("a stored null has no fields and is not missing")
	}
	if !Missing(row([]query.Column{{Name: "x"}}, doc)[0]) {
		t.Error("an absent field must be missing")
	}
}

func TestParseCommand(t *testing.T) {
	cmd, warn, err := Parse(` {"find": "users", "filter": {"age": {"$gt": 3}}} `, true)
	if err != nil || cmd.Name != "find" || len(warn) != 0 {
		t.Fatalf("find = %v %v %v", cmd.Name, warn, err)
	}
	for _, text := range []string{
		`{"insert": "users", "documents": [{}]}`,
		`{"aggregate": "users", "pipeline": [{"$out": "x"}], "cursor": {}}`,
	} {
		if _, _, err := Parse(text, true); err == nil {
			t.Errorf("read-only accepted %s", text)
		}
	}
	if _, warn, _ := Parse(`{"delete": "users", "deletes": [{"q": {}, "limit": 0}]}`, false); len(warn) != 1 {
		t.Errorf("delete without filter: warnings = %v", warn)
	}
	if _, warn, _ := Parse(`{"update": "users", "updates": [{"q": {"a": 1}, "u": {"$set": {"b": 2}}}]}`, false); len(warn) != 0 {
		t.Errorf("update with filter: warnings = %v", warn)
	}
	for _, text := range []string{"", "db.users.find()", "{}"} {
		if _, _, err := Parse(text, false); err == nil {
			t.Errorf("Parse(%q) accepted", text)
		}
	}
}

func TestURI(t *testing.T) {
	c := models.DBConn{Host: "::1", Port: 27017}
	if got := URI(c); got != "mongodb://[::1]:27017/" {
		t.Errorf("URI = %s", got)
	}
	c.Host = "mongodb+srv://cluster.example.net/?authSource=admin"
	if got := URI(c); got != c.Host {
		t.Errorf("URI = %s", got)
	}
}
