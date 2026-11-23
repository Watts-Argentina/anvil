package ui

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
	"github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend/soft"
)

// TestMongo needs ANVIL_TEST_MONGO (a connection string, see
// internal/mongodb/server_test.go).
func TestMongo(t *testing.T) {
	uri := os.Getenv("ANVIL_TEST_MONGO")
	if uri == "" {
		t.Skip("ANVIL_TEST_MONGO not set")
	}
	a := testApp(t)
	ctx := context.Background()
	res, err := a.Conns.Save(ctx, models.DBConn{Name: "docs", Engine: models.EngineMongo, Host: uri, Color: "#46A758"}, "")
	if err != nil {
		t.Fatal(err)
	}
	a.Connections, _ = a.Conns.List(ctx)
	w := newTestWindow(t, a)
	id := res.Conn.ID

	// No user: it connects without asking for a password and lists the
	// databases.
	connect(w, id)
	waitFor(t, w, func() bool { return a.Databases[id] != nil })
	s, _ := a.Sessions.Get(id)
	name := fmt.Sprintf("anvil_ui_%d", time.Now().UnixNano())
	t.Cleanup(func() { s.MongoDB(name).Drop(context.Background()) })
	key := dbKey{ConnID: id, Database: name}

	// The editor runs commands.
	ed := openSQLTab(w, a, key)
	run := func(cmd string) {
		t.Helper()
		ed.Run = nil
		runSQL(w, ed, cmd)
		waitFor(t, w, func() bool { return ed.Run != nil })
		if ed.Err != "" {
			t.Fatalf("%s: %s", cmd, ed.Err)
		}
	}
	run(`{"insert": "people", "documents": [
		{"_id": 1, "name": "Ana", "age": 30, "born": {"$date": "1994-01-02T00:00:00Z"}},
		{"_id": 2, "name": "Bruno", "tags": ["x", "y"], "addr": {"city": "Rosario", "geo": {"lat": -32.9, "lng": -60.6}}}]}`)
	if ed.Run.Affected() != 2 {
		t.Fatalf("inserted %d", ed.Run.Affected())
	}
	run(`{"find": "people", "filter": {}}`)
	if ed.Page == nil || len(ed.Page.Rows) != 2 {
		t.Fatalf("find = %+v", ed.Page)
	}
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "mongo-editor")

	// A delete without filter asks first.
	runSQL(w, ed, `{"delete": "people", "deletes": [{"q": {}, "limit": 0}]}`)
	if !w.DialogIsVisible() {
		t.Fatal("expected confirmation dialog")
	}
	w.DialogDismiss()

	// The collection opens in the grid, with _id as the key.
	toggleDatabase(w, key)
	waitFor(t, w, func() bool { return len(a.Tables[key]) == 1 })
	w.TestRender(nil)
	scope := gui.ScopeIDN("conn", "", int(id))
	for _, leaf := range []string{"new-table", "new-db"} {
		for _, found := range w.ResolveID(leaf) {
			if strings.HasPrefix(found, scope) {
				t.Errorf("MongoDB has no %s form: %s", leaf, found)
			}
		}
	}
	openTable(w, key, a.Tables[key][0], false)
	waitFor(t, w, func() bool { return a.table().Data.fetched })
	tv := a.table()
	if !tv.editable(a) || !tv.hasPK() {
		t.Fatal("the collection should be editable by _id")
	}
	cols := make([]string, len(tv.Data.Page.Columns))
	for i, c := range tv.Data.Page.Columns {
		cols[i] = c.Name
	}
	if !slices.Equal(cols, []string{"_id", "name", "age", "born", "tags", "addr"}) {
		t.Fatalf("columns = %v", cols)
	}

	// The Documents view is the default: nested fields expand, read-only.
	if tv.Data.AsTable {
		t.Fatal("MongoDB opens in the Documents view")
	}
	toggleField(tv, docKey{Row: 1, Path: "addr"})
	toggleField(tv, docKey{Row: 1, Path: "addr\x00geo"})
	w.TestRender(nil)
	soft.RenderToImage(w, 1)
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "mongo-docs")
	// A click on a top-level field edits it, as in the grid.
	clickCell(tv, cellRef{Row: 0, Column: "name"})(gui.EventCtx{Window: w})
	w.FrameFn()
	w.TestRender(nil)
	if e := tv.Data.Edit; !e.Active || e.Text != "Ana" {
		t.Fatalf("edit = %+v", e)
	}
	if err := w.TestType(editID, "!"); err != nil {
		t.Fatal(err)
	}
	if err := w.TestKey(editID, gui.KeyEnter, 0); err != nil {
		t.Fatal(err)
	}
	if v, ok := tv.Data.Pending.Value(0, "name"); !ok || !strings.Contains(v.Text, "!") {
		t.Fatalf("name = %+v %v", v, ok)
	}
	discardChanges(tv)

	// Edit, add and save through the review dialog.
	p := &tv.Data.Pending
	p.Set(0, "age", editing.Text("31"), editing.Text("30"))
	newID := p.AddRow()
	p.SetNew(newID, "name", editing.Text("Carla"))
	reviewSave(w, tv, nil)
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "mongo-save")
	clickDialog(t, w, "save-confirm")
	waitFor(t, w, func() bool { return p.Len() == 0 && !tv.Data.Loading || tv.Data.SaveErr != "" })
	if tv.Data.SaveErr != "" {
		t.Fatal(tv.Data.SaveErr)
	}
	var got []string
	for _, r := range tv.Data.Page.Rows {
		got = append(got, strings.Join(query.Values(r[1:3]), "|"))
	}
	if !slices.Equal(got[:2], []string{"Ana|31", "Bruno|"}) || len(got) != 3 {
		t.Fatalf("rows after save = %v", got)
	}
	tv.Data.AsTable = true
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "mongo-grid")

	// Read-only connections reject writes from the editor.
	s.Conn.ReadOnly = true
	runSQL(w, ed, `{"drop": "people"}`)
	if !strings.Contains(ed.Err, "read-only") {
		t.Fatalf("read-only drop: %q", ed.Err)
	}
}
