package mongodb

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Watts-Argentina/anvil/internal/editing"
	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/models"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Against real servers, if the URIs are set (see the anvil-conventions skill):
//
//	docker run -d --rm --name anvil-mongo -p 127.0.0.1:57017:27017 mongo:8 --replSet rs0 --bind_ip_all
//	docker exec anvil-mongo mongosh --quiet --eval 'rs.initiate()'
//	docker run -d --rm --name anvil-mongo-sa -p 127.0.0.1:57018:27017 mongo:8
//	ANVIL_TEST_MONGO="mongodb://127.0.0.1:57017/?directConnection=true" \
//	ANVIL_TEST_MONGO_STANDALONE="mongodb://127.0.0.1:57018/" go test ./internal/mongodb/
func servers(t *testing.T) map[string]string {
	out := map[string]string{}
	for name, env := range map[string]string{"replset": "ANVIL_TEST_MONGO", "standalone": "ANVIL_TEST_MONGO_STANDALONE"} {
		if uri := os.Getenv(env); uri != "" {
			out[name] = uri
		}
	}
	if len(out) == 0 {
		t.Skip("ANVIL_TEST_MONGO not set")
	}
	return out
}

// testColl connects and fills its own database with n documents
// {_id: i, name: "n<i>", age: i}.
func testColl(t *testing.T, uri string, n int) (*mongo.Client, *mongo.Collection) {
	t.Helper()
	ctx := context.Background()
	client, err := Connect(ctx, models.DBConn{Engine: models.EngineMongo, Host: uri}, "")
	if err != nil {
		t.Fatal(err)
	}
	db := client.Database(fmt.Sprintf("anvil_test_%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		db.Drop(ctx)
		client.Disconnect(ctx)
	})
	coll := db.Collection("people")
	docs := make([]any, n)
	for i := range docs {
		docs[i] = bson.D{{Key: "_id", Value: int32(i)}, {Key: "name", Value: fmt.Sprintf("n%d", i)}, {Key: "age", Value: int32(i)}}
	}
	if _, err := coll.InsertMany(ctx, docs); err != nil {
		t.Fatal(err)
	}
	return client, coll
}

func TestServerBrowse(t *testing.T) {
	ctx := context.Background()
	for name, uri := range servers(t) {
		t.Run(name, func(t *testing.T) {
			client, coll := testColl(t, uri, 25)
			dbs, err := ListDatabases(ctx, client)
			if err != nil || !slices.Contains(dbs, coll.Database().Name()) {
				t.Fatalf("ListDatabases = %v, %v", dbs, err)
			}
			tables, err := ListCollections(ctx, coll.Database())
			if err != nil || len(tables) != 1 || tables[0].Name != "people" {
				t.Fatalf("ListCollections = %v, %v", tables, err)
			}
			cols, err := Columns(ctx, coll)
			if err != nil || len(cols) != 3 || cols[2].DBType != "int" {
				t.Fatalf("Columns = %v, %v", cols, err)
			}

			p, err := LoadPage(ctx, coll, cols, query.Page{}, query.First, 10)
			if err != nil || len(p.Rows) != 10 || !p.HasNext {
				t.Fatalf("first page = %d rows, next %v, %v", len(p.Rows), p.HasNext, err)
			}
			p, _ = LoadPage(ctx, coll, cols, p, query.Next, 10)
			p, _ = LoadPage(ctx, coll, cols, p, query.Next, 10)
			if len(p.Rows) != 5 || p.HasNext || p.Rows[0][1].Text != "n20" {
				t.Fatalf("last page = %v, next %v", p.Rows, p.HasNext)
			}
			if n, err := Count(ctx, coll); n != 25 || err != nil {
				t.Errorf("Count = %d, %v", n, err)
			}

			// A field out of the sampled columns still reaches the page and
			// the export.
			coll.InsertOne(ctx, bson.D{{Key: "_id", Value: int32(99)}, {Key: "extra", Value: "e"}})
			p, _ = LoadPage(ctx, coll, cols, p, query.Reload, 10)
			if got := p.Columns[len(p.Columns)-1].Name; got != "extra" {
				t.Errorf("page columns end with %s, want extra", got)
			}
			var sink rowSink
			if err := Stream(ctx, coll, cols, &sink); err != nil {
				t.Fatal(err)
			}
			if len(sink.rows) != 26 || strings.Join(sink.header, ",") != "_id,name,age,extra" {
				t.Errorf("Stream = %v, %d rows", sink.header, len(sink.rows))
			}
		})
	}
}

func TestServerEdit(t *testing.T) {
	ctx := context.Background()
	for name, uri := range servers(t) {
		t.Run(name, func(t *testing.T) {
			client, coll := testColl(t, uri, 3)
			tx := Transactions(ctx, client)
			if tx != (name == "replset") {
				t.Fatalf("Transactions = %v on %s", tx, name)
			}
			cols, _ := Columns(ctx, coll)
			page, _ := LoadPage(ctx, coll, cols, query.Page{}, query.First, 10)

			var p editing.Pending
			p.Set(0, "age", editing.Text("40"), editing.Text("0"))
			p.Set(1, "name", editing.Null, editing.Text("n1"))
			id := p.AddRow()
			p.SetNew(id, "name", editing.Text("new"))
			p.Delete(2)
			writes, err := Build(cols, page.Result, p.Changes())
			if err != nil {
				t.Fatal(err)
			}
			if got := writes[0].Preview("people"); got != `db.getCollection("people").updateOne({"_id":0}, {"$set":{"age":40}})` {
				t.Errorf("Preview = %s", got)
			}
			if err := Apply(ctx, coll, writes, tx); err != nil {
				t.Fatal(err)
			}
			var got bson.M
			coll.FindOne(ctx, bson.D{{Key: "_id", Value: 0}}).Decode(&got)
			if got["age"] != int32(40) {
				t.Errorf("age = %#v, want int32 40", got["age"])
			}
			if n, _ := coll.CountDocuments(ctx, bson.D{{Key: "name", Value: "new"}}); n != 1 {
				t.Error("the new document was not inserted")
			}
			if n, _ := coll.CountDocuments(ctx, bson.D{{Key: "_id", Value: 2}}); n != 0 {
				t.Error("the document was not deleted")
			}

			// The deleted document is gone: deleting it again fails, and the
			// update before it is rolled back only with transactions.
			p.Clear()
			p.Set(0, "age", editing.Text("50"), editing.Text("40"))
			p.Delete(2)
			writes, _ = Build(cols, page.Result, p.Changes())
			if err := Apply(ctx, coll, writes, tx); err == nil {
				t.Fatal("deleting a missing document must fail")
			}
			coll.FindOne(ctx, bson.D{{Key: "_id", Value: 0}}).Decode(&got)
			if want := map[bool]int32{true: 40, false: 50}[tx]; got["age"] != want {
				t.Errorf("age after the failure = %v, want %d", got["age"], want)
			}

			if _, err := Build(cols, page.Result, []editing.Change{{Kind: editing.Update, Row: 0, Values: map[string]editing.Value{"age": editing.Text("x")}}}); err == nil {
				t.Error("an int field accepted x")
			}
		})
	}
}

func TestServerRun(t *testing.T) {
	ctx := context.Background()
	for name, uri := range servers(t) {
		t.Run(name, func(t *testing.T) {
			_, coll := testColl(t, uri, 12)
			db := coll.Database()
			run := func(text string) query.RunResult {
				t.Helper()
				cmd, _, err := Parse(text, false)
				if err != nil {
					t.Fatal(err)
				}
				res, err := Run(ctx, db, cmd, 5)
				if err != nil {
					t.Fatalf("%s: %v", text, err)
				}
				return res
			}

			res := run(`{"find": "people", "filter": {"age": {"$gte": 2}}, "sort": {"_id": 1}}`)
			if res.Rows == nil || len(res.Rows.Rows) != 5 || !res.Rows.HasNext || !res.Rows.Repeatable {
				t.Fatalf("find = %+v", res.Rows)
			}
			page, err := RunPage(ctx, db, *res.Rows, 1)
			if err != nil || len(page.Rows) != 5 || page.Rows[0][0].Text != "7" {
				t.Fatalf("page 2 = %v, %v", page.Rows, err)
			}
			var sink rowSink
			if err := StreamRun(ctx, db, *res.Rows, &sink); err != nil || len(sink.rows) != 10 {
				t.Errorf("StreamRun = %d rows, %v", len(sink.rows), err)
			}

			res = run(`{"update": "people", "updates": [{"q": {"age": {"$lt": 3}}, "u": {"$set": {"young": true}}, "multi": true}]}`)
			if res.Rows != nil || res.Affected() != 3 {
				t.Errorf("update affected %d", res.Affected())
			}
			res = run(`{"count": "people"}`)
			if res.Rows == nil || len(res.Rows.Rows) != 1 {
				t.Errorf("count = %+v", res.Rows)
			}

			cmd, _, _ := Parse(`{"insert": "people", "documents": [{"_id": 1}]}`, false)
			if _, err := Run(ctx, db, cmd, 5); err == nil || !strings.Contains(err.Error(), "duplicate") && !strings.Contains(strings.ToLower(fmt.Sprint(err)), "e11000") {
				t.Errorf("duplicate insert: %v", err)
			}
		})
	}
}

type rowSink struct {
	header []string
	rows   [][]string
}

func (s *rowSink) Header(cols []query.Column) error {
	for _, c := range cols {
		s.header = append(s.header, c.Name)
	}
	return nil
}

func (s *rowSink) Row(row []query.Value) error {
	s.rows = append(s.rows, query.Values(row))
	return nil
}
