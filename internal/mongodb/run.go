package mongodb

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/pkg/appErrors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// The editor runs database commands written as relaxed Extended JSON
// (SPEC §5.3): {"find": "users", "filter": {"age": {"$gt": 30}}}. The
// first key is the command.

// cursorCommands return a cursor: their documents are paginated.
var cursorCommands = []string{"find", "aggregate", "listCollections", "listIndexes"}

// readCommands are the ones a read-only connection accepts. aggregate is
// accepted unless it writes with $out or $merge.
var readCommands = []string{
	"find", "aggregate", "count", "distinct", "listCollections", "listIndexes", "listDatabases",
	"collStats", "dbStats", "explain", "ping", "hello", "buildInfo", "serverStatus", "connectionStatus",
}

// writeCommands report the documents they affected in "n".
var writeCommands = []string{"insert", "update", "delete"}

// Command is a parsed editor command.
type Command struct {
	Name string
	Doc  bson.D
	Text string
}

// Parse reads the command and runs the pre-checks (SPEC §8): on a
// read-only connection only reads pass, and update/delete statements with
// an empty filter are warned about.
func Parse(text string, readOnly bool) (Command, []string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Command{}, nil, appErrors.Error("Nothing to run.", `Write a command first, for example {"find": "users", "filter": {}}.`, fmt.Errorf("empty command"))
	}
	var doc bson.D
	if err := bson.UnmarshalExtJSON([]byte(text), false, &doc); err != nil {
		return Command{}, nil, appErrors.Error("Invalid command.",
			`Commands are one JSON document whose first key is the command, for example {"find": "users", "filter": {}}: `+err.Error(), err)
	}
	if len(doc) == 0 {
		return Command{}, nil, appErrors.Error("Invalid command.", "The document is empty: its first key must be the command.", fmt.Errorf("empty command document"))
	}
	cmd := Command{Name: doc[0].Key, Doc: doc, Text: text}

	writesAgg := cmd.Name == "aggregate" && aggregateWrites(doc)
	if readOnly && (!slices.Contains(readCommands, cmd.Name) || writesAgg) {
		return Command{}, nil, appErrors.Error("Read-only connection.",
			fmt.Sprintf("This connection is read-only and %q is not a read command.", cmd.Name),
			fmt.Errorf("write command on read-only connection"))
	}

	var warnings []string
	if cmd.Name == "update" || cmd.Name == "delete" {
		key := map[string]string{"update": "updates", "delete": "deletes"}[cmd.Name]
		for _, st := range array(doc, key) {
			if q, ok := st.(bson.D); ok && emptyFilter(q) {
				warnings = append(warnings, fmt.Sprintf("%s with an empty filter changes every document of %v.", cmd.Name, doc[0].Value))
				break
			}
		}
	}
	return cmd, warnings, nil
}

// field returns a field of a document.
func field(d bson.D, key string) (any, bool) {
	for _, e := range d {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

func array(d bson.D, key string) bson.A {
	v, _ := field(d, key)
	a, _ := v.(bson.A)
	return a
}

// emptyFilter reports whether an update/delete statement has q: {}.
func emptyFilter(st bson.D) bool {
	q, ok := field(st, "q")
	d, isDoc := q.(bson.D)
	return !ok || isDoc && len(d) == 0
}

func aggregateWrites(d bson.D) bool {
	for _, stage := range array(d, "pipeline") {
		if s, ok := stage.(bson.D); ok && len(s) > 0 && (s[0].Key == "$out" || s[0].Key == "$merge") {
			return true
		}
	}
	return false
}

// Run runs the command. Cursor commands give a page of size documents;
// insert/update/delete give the documents affected; any other command
// gives its reply as a single row.
func Run(ctx context.Context, db *mongo.Database, cmd Command, size int) (query.RunResult, error) {
	res := query.RunResult{Failed: -1}
	start := time.Now()
	sr := query.StmtResult{SQL: cmd.Text}
	defer func() { res.Duration = time.Since(start) }()

	// fail takes an error already wrapped for the UI.
	fail := func(err error) (query.RunResult, error) {
		sr.Err, sr.Duration = err, time.Since(start)
		res.Statements, res.Failed = []query.StmtResult{sr}, 0
		return res, err
	}

	if slices.Contains(cursorCommands, cmd.Name) {
		page, err := RunPage(ctx, db, query.RowsPage{SQL: cmd.Text, Size: size, Repeatable: !aggregateWrites(cmd.Doc)}, 0)
		if err != nil {
			return fail(err)
		}
		sr.HasRows, sr.Rows, sr.Duration = true, len(page.Rows), time.Since(start)
		res.Statements, res.Rows = []query.StmtResult{sr}, &page
		return res, nil
	}

	var reply bson.Raw
	if err := db.RunCommand(ctx, cmd.Doc).Decode(&reply); err != nil {
		return fail(engineError("Command failed.", err))
	}
	sr.Duration = time.Since(start)
	if slices.Contains(writeCommands, cmd.Name) {
		// The command is ok even if some of its writes failed.
		if we, err := reply.LookupErr("writeErrors"); err == nil {
			err := errors.New("write error: " + extJSON(we))
			return fail(appErrors.Error("Command failed.", err.Error(), err))
		}
		if n, ok := reply.Lookup("n").AsInt64OK(); ok {
			sr.Affected = n
		}
		res.Statements = []query.StmtResult{sr}
		return res, nil
	}
	sr.HasRows, sr.Rows = true, 1
	page := query.RowsPage{Result: result(nil, []bson.Raw{reply}), SQL: cmd.Text, Size: size}
	res.Statements, res.Rows = []query.StmtResult{sr}, &page
	return res, nil
}

// RunPage runs the cursor command of page again and reads page index.
//
// shortcut: skips the earlier documents on the client, O(offset); add
// skip/limit to find and $skip/$limit to aggregate if deep pages get slow.
func RunPage(ctx context.Context, db *mongo.Database, page query.RowsPage, index int) (query.RowsPage, error) {
	c, err := cursor(ctx, db, page.SQL)
	if err != nil {
		return page, err
	}
	defer c.Close(ctx)

	var docs []bson.Raw
	skip, more := index*page.Size, false
	for n := 0; c.Next(ctx); n++ {
		switch {
		case n < skip:
		case len(docs) == page.Size:
			more = true
		default:
			docs = append(docs, slices.Clone(c.Current))
		}
		if more {
			break
		}
	}
	if err := c.Err(); err != nil {
		return page, engineError("Command failed.", err)
	}
	page.Result, page.Index, page.HasNext = result(nil, docs), index, more
	return page, nil
}

// StreamRun runs the cursor command of page twice: once to collect every
// field for the header, then to send every document (export, SPEC §9).
func StreamRun(ctx context.Context, db *mongo.Database, page query.RowsPage, sink query.RowSink) error {
	if !page.Repeatable {
		return appErrors.Error("Cannot export the full result.", "This result cannot be read again; export the current page instead.", fmt.Errorf("non repeatable result"))
	}
	c, err := cursor(ctx, db, page.SQL)
	if err != nil {
		return err
	}
	var cols []query.Column
	for c.Next(ctx) {
		for _, f := range fields([]bson.Raw{c.Current}) {
			if !slices.ContainsFunc(cols, func(x query.Column) bool { return x.Name == f.Name }) {
				cols = append(cols, query.Column{Name: f.Name, DBType: f.DBType})
			}
		}
	}
	err = c.Err()
	c.Close(ctx)
	if err != nil {
		return engineError("Failed to read the documents.", err)
	}
	if c, err = cursor(ctx, db, page.SQL); err != nil {
		return err
	}
	return streamCursor(ctx, c, cols, sink)
}

func cursor(ctx context.Context, db *mongo.Database, text string) (*mongo.Cursor, error) {
	var doc bson.D
	if err := bson.UnmarshalExtJSON([]byte(text), false, &doc); err != nil {
		return nil, appErrors.Error("Invalid command.", err.Error(), err)
	}
	c, err := db.RunCommandCursor(ctx, doc)
	if err != nil {
		return nil, engineError("Command failed.", err)
	}
	return c, nil
}
