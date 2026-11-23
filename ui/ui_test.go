package ui

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Watts-Argentina/anvil/internal/completion"
	"github.com/Watts-Argentina/anvil/internal/connections"
	"github.com/Watts-Argentina/anvil/internal/drivers"
	"github.com/Watts-Argentina/anvil/internal/editing"
	"github.com/Watts-Argentina/anvil/internal/history"
	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/internal/schema"
	"github.com/Watts-Argentina/anvil/internal/settings"
	"github.com/Watts-Argentina/anvil/internal/storage"
	"github.com/Watts-Argentina/anvil/models"
	_ "github.com/glebarez/go-sqlite"
	"github.com/go-gui-org/go-edit/edit/buffer"
	"github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend/soft"
	"github.com/go-gui-org/go-gui/gui/svg"
	"github.com/zalando/go-keyring"
)

// testApp builds an App with an open SQLite connection and a closed PG one.
func testApp(t *testing.T) *App {
	t.Helper()
	keyring.MockInit()
	ctx := context.Background()

	db, err := storage.Open("file:" + t.Name() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	a := newApp(&connections.Service{DB: db}, &settings.Service{DB: db}, &history.Service{DB: db})

	path := filepath.Join(t.TempDir(), "shop.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"create table customers (id integer primary key, name text not null, email text, created timestamp)",
		"create table orders (id integer primary key, customer_id integer references customers (id), total real)",
		"create view big_orders as select * from orders where total > 100",
		"insert into customers (name, email) values ('Ana', 'ana@example.com'), ('Bruno', NULL), ('Carla', '')",
		"update customers set created = '2024-03-01 10:20:30' where id = 1",
		"insert into orders (customer_id, total) values (1, 50), (1, 250.5), (2, 120)",
		"create table wide (id integer primary key, c01 text, c02 text, c03 text, c04 text, c05 text, c06 text, c07 text, c08 text, c09 text, c10 text, last_column text)",
		"insert into wide values (1, 'alpha', 'beta', 'gamma', 'delta', 'epsilon', 'zeta', 'eta', 'theta', 'iota', 'kappa', 'end')",
		"insert into wide values (2, " + strings.Repeat("'"+strings.Repeat("long text ", 20)+"', ", 10) + "'end')",
	} {
		if _, err := raw.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()

	lite, err := a.Conns.Save(ctx, models.DBConn{Name: "shop.db", Engine: models.EngineSQLite, DB: path, Color: "#46A758"}, "")
	if err != nil {
		t.Fatal(err)
	}
	pg := models.DBConn{Name: "prod-pg", Engine: models.EnginePostgres, Host: "db", Port: 5432, User: "app", Color: "#0090FF", ReadOnly: true}
	if _, err := a.Conns.Save(ctx, pg, ""); err != nil {
		t.Fatal(err)
	}
	if a.Connections, err = a.Conns.List(ctx); err != nil {
		t.Fatal(err)
	}

	s, err := a.Sessions.Connect(ctx, lite.Conn, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Sessions.CloseAll)
	id := lite.Conn.ID
	a.Selected = id
	a.Expanded[id] = true
	key := dbKey{ConnID: id}
	if a.Tables[key], err = s.Tables(ctx, ""); err != nil {
		t.Fatal(err)
	}
	cols, err := s.Columns(ctx, "", a.Tables[key][1])
	if err != nil {
		t.Fatal(err)
	}
	a.newTab(&tab{Key: key, Sub: tabData, Table: &tableView{
		Key: key, Table: a.Tables[key][1], Columns: cols, Data: dataState{Size: a.Prefs.DefaultPageSize},
	}})
	return a
}

func newTestWindow(t *testing.T, a *App) *gui.Window {
	w := gui.NewTestWindow(t, gui.WindowCfg{State: a, Width: 1100, Height: 700})
	w.SetSvgParser(svg.New()) // the real backends set it up on their own
	w.TestRender(mainView)
	return w
}

// screenshot saves a PNG if ANVIL_SCREENSHOTS points to a directory.
func screenshot(t *testing.T, w *gui.Window, name string) {
	dir := os.Getenv("ANVIL_SCREENSHOTS")
	if dir == "" {
		return
	}
	if err := soft.RenderToPNG(w, 1, filepath.Join(dir, name+".png")); err != nil {
		t.Fatal(err)
	}
}

func checkFindings(t *testing.T, w *gui.Window) {
	t.Helper()
	if found := w.TestFindings(gui.DebugAll); len(found) > 0 {
		t.Fatalf("UI defects:\n%v", found)
	}
}

func TestMainView(t *testing.T) {
	for _, theme := range []string{models.ThemeDark, models.ThemeLight} {
		t.Run(theme, func(t *testing.T) {
			a := testApp(t)
			a.Prefs.Theme = theme
			applyTheme(nil, theme)
			t.Cleanup(func() { applyTheme(nil, models.ThemeDark) })
			w := newTestWindow(t, a)
			checkFindings(t, w)
			screenshot(t, w, "main-"+theme)
		})
	}
}

func TestConnForm(t *testing.T) {
	a := testApp(t)
	w := newTestWindow(t, a)

	openConnForm(w, models.DBConn{})
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "conn-form-pg")

	// Switching to SQLite shows the file picker.
	a.Form.Conn.Engine = models.EngineSQLite
	w.TestRender(nil)
	if ids := w.ResolveID("cf-browse"); len(ids) != 1 {
		t.Fatalf("browse button not rendered: %v", ids)
	}
	screenshot(t, w, "conn-form-sqlite")
}

func TestSidebarClickSelectsTable(t *testing.T) {
	a := testApp(t)
	w := newTestWindow(t, a)
	dropTab(a, a.Active)
	w.TestRender(nil)

	id := gui.ScopeIDN(gui.ScopeIDN("conn", "", int(a.Selected)), "table", 0)
	if err := w.TestClick(id); err != nil {
		t.Fatal(err)
	}
	if a.table() == nil || a.table().Table.Name != "big_orders" {
		t.Fatalf("table not opened: %+v", a.table())
	}
}

// TestAllDatabases needs ANVIL_TEST_PG="host port user password database".
func TestAllDatabases(t *testing.T) {
	v := strings.Fields(os.Getenv("ANVIL_TEST_PG"))
	if len(v) != 5 {
		t.Skip("ANVIL_TEST_PG not set")
	}
	a := testApp(t)
	ctx := context.Background()
	port, _ := strconv.Atoi(v[1])
	res, err := a.Conns.Save(ctx, models.DBConn{
		Name: "cluster", Engine: models.EnginePostgres, Host: v[0], Port: port,
		User: v[2], SSLMode: "disable", Color: "#8E4EC6",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	a.Connections, _ = a.Conns.List(ctx)
	w := newTestWindow(t, a)

	// Same path as the password prompt, waiting for the async.
	openSession(w, res.Conn, v[3], false)
	waitFor(t, w, func() bool { return len(a.Databases[res.Conn.ID]) > 0 })
	if !slices.Contains(a.Databases[res.Conn.ID], "postgres") {
		t.Fatalf("databases = %v", a.Databases[res.Conn.ID])
	}

	key := dbKey{ConnID: res.Conn.ID, Database: v[4]}
	toggleDatabase(w, key)
	waitFor(t, w, func() bool { return a.Tables[key] != nil || !a.Loading[key] })
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "all-databases")

	// New database: created, listed and expanded.
	name := fmt.Sprintf("anvil_newdb_%d", time.Now().UnixNano())
	s, _ := a.Sessions.Get(res.Conn.ID)
	t.Cleanup(func() { s.DB.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`) })
	if err := w.TestClick(gui.ScopeID(gui.ScopeIDN("conn", "", int(res.Conn.ID)), "new-db")); err != nil {
		t.Fatal(err)
	}
	a.NewDB.Name = name
	w.TestRender(nil)
	checkFindings(t, w)
	clickDialog(t, w, "nd-create")
	newKey := dbKey{ConnID: res.Conn.ID, Database: name}
	waitFor(t, w, func() bool { return a.DBExpanded[newKey] && !a.Loading[newKey] })
	if !slices.Contains(a.Databases[res.Conn.ID], name) {
		t.Fatalf("databases = %v", a.Databases[res.Conn.ID])
	}
}

func TestNewTable(t *testing.T) {
	a := testApp(t)
	w := newTestWindow(t, a)
	scope := gui.ScopeIDN("conn", "", int(a.Selected))
	if err := w.TestClick(gui.ScopeID(scope, "new-table")); err != nil {
		t.Fatal(err)
	}
	w.TestRender(nil)
	f := &a.NewTable
	if len(f.Columns) != 2 || !f.Columns[0].AutoInc {
		t.Fatalf("initial columns: %+v", f.Columns)
	}
	checkFindings(t, w)

	// Repeated column names keep Create disabled.
	f.Name = "notes"
	f.Columns[1].Name = "ID"
	w.TestRender(nil)
	if err := w.TestClick(w.ResolveID("nt-create")[0]); err == nil || f.Busy {
		t.Fatal("Create is enabled for an invalid table")
	}

	f.Columns[1].Name = "body"
	f.addColumn(schemaColumn("at", "timestamp", "CURRENT_TIMESTAMP"))
	// Unchecking PK also clears auto-increment.
	clickDialog(t, w, gui.ScopeID(gui.ScopeIDN("nt-col", "", f.Columns[0].ID), "pk"))
	if f.Columns[0].PK || f.Columns[0].AutoInc {
		t.Fatalf("pk toggle: %+v", f.Columns[0])
	}
	clickDialog(t, w, gui.ScopeID(gui.ScopeIDN("nt-col", "", f.Columns[0].ID), "pk"))
	clickDialog(t, w, gui.ScopeID(gui.ScopeIDN("nt-col", "", f.Columns[1].ID), "null"))
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "new-table")

	clickDialog(t, w, "nt-create")
	waitFor(t, w, func() bool { return a.table() != nil && a.table().Table.Name == "notes" && !a.table().Loading })
	cols := a.table().Columns
	if len(cols) != 3 || !cols[0].PK || cols[1].Nullable || !cols[2].Nullable {
		t.Fatalf("columns: %+v", cols)
	}
	if !slices.ContainsFunc(a.Tables[a.table().Key], func(tb drivers.Table) bool { return tb.Name == "notes" }) {
		t.Fatal("sidebar not reloaded")
	}
}

func schemaColumn(name, typ, def string) schema.Column {
	return schema.Column{Name: name, Type: typ, Default: def, Nullable: true}
}

// waitFor processes frames until cond holds (the asyncs come back through
// QueueCommand).
// openSQLTab opens an SQL tab on key and returns its editor.
func openSQLTab(w *gui.Window, a *App, key dbKey) *sqlEditor {
	newSQLTab(w, key)
	return a.Active.SQL
}

func waitFor(t *testing.T, w *gui.Window, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for async work")
		}
		time.Sleep(20 * time.Millisecond)
		w.FrameFn() // runs the pending QueueCommands
	}
}

func TestTableTabs(t *testing.T) {
	a := testApp(t)
	w := newTestWindow(t, a)
	key := a.table().Key

	// openTable loads the columns and the first data page.
	openTable(w, key, a.Tables[key][1], false) // customers
	waitFor(t, w, func() bool { return a.table().Data.fetched })
	if got := len(a.table().Data.Page.Rows); got != 3 {
		t.Fatalf("rows = %d (%s)", got, a.table().Data.Err)
	}
	if !a.table().Data.Page.Rows[1][2].Null || a.table().Data.Page.Rows[2][2].Null {
		t.Fatal("NULL and empty string must differ")
	}

	for _, tab := range []string{tabStructure, tabData} {
		selectTab(w, tab)
		w.TestRender(nil)
		checkFindings(t, w)
		screenshot(t, w, "tab-"+tab)
	}

	// Grid borders and header in the light theme.
	t.Cleanup(func() { applyTheme(w, models.ThemeDark) })
	applyTheme(w, models.ThemeLight)
	selectTab(w, tabData)
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "tab-data-light")
}

func TestFollowForeignKey(t *testing.T) {
	a := testApp(t)
	w := newTestWindow(t, a)
	key := a.table().Key

	openTable(w, key, a.Tables[key][2], false) // orders
	waitFor(t, w, func() bool { return a.table().Data.fetched })
	orders := a.table()
	if len(orders.FKs) != 1 || orders.FKs[0].RefTable.Name != "customers" {
		t.Fatalf("foreign keys = %+v", orders.FKs)
	}
	w.TestRender(nil)
	soft.RenderToImage(w, 1)
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "fk-orders")

	// Row 2 (customer_id = 2), column 1: the link opens customer 2.
	if err := w.TestClick(gui.ScopeIDN("data-grid", "link", 2*3+1)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, w, func() bool { return a.table() != orders && a.table().Data.fetched })
	if a.table().Table.Name != "customers" || a.table().Back != orders {
		t.Fatalf("opened %+v", a.table().Table)
	}
	if rows := a.table().Data.Page.Rows; len(rows) != 1 || rows[0][1].Text != "Bruno" {
		t.Fatalf("customer rows = %v (%s)", rows, a.table().Data.Err)
	}
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "fk-customer")

	ids := w.ResolveID("table-back")
	if len(ids) != 1 {
		t.Fatalf("back button ids = %v", ids)
	}
	if err := w.TestClick(ids[0]); err != nil {
		t.Fatal(err)
	}
	if a.table() != orders {
		t.Fatal("back did not return to orders")
	}
}

// clickDialog clicks a button of the open dialog by its leaf ID.
func clickDialog(t *testing.T, w *gui.Window, leaf string) {
	t.Helper()
	w.TestRender(nil)
	ids := w.ResolveID(leaf)
	if len(ids) != 1 {
		t.Fatalf("%s ids = %v", leaf, ids)
	}
	if err := w.TestClick(ids[0]); err != nil {
		t.Fatal(err)
	}
}

// typeCell edits a cell: opens its input, types text and presses key.
func typeCell(t *testing.T, w *gui.Window, tv *tableView, ref cellRef, text string, key gui.KeyCode, mods gui.Modifier) {
	t.Helper()
	startEdit(w, tv, ref)
	w.FrameFn() // runs the queued SetFocus
	w.TestRender(nil)
	if !tv.Data.Edit.Active {
		t.Fatalf("not editing %+v", ref)
	}
	if text != "" {
		if err := w.TestType(editID, text); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.TestKey(editID, key, mods); err != nil {
		t.Fatal(err)
	}
	w.TestRender(nil)
}

func TestEditRows(t *testing.T) {
	a := testApp(t)
	w := newTestWindow(t, a)
	key := a.table().Key

	// Views are read-only.
	openTable(w, key, a.Tables[key][0], false) // big_orders
	waitFor(t, w, func() bool { return a.table().Data.fetched })
	if a.table().editable(a) {
		t.Fatal("a view must not be editable")
	}

	openTable(w, key, a.Tables[key][1], false) // customers
	waitFor(t, w, func() bool { return a.table().Data.fetched })
	tv := a.table()
	if !tv.editable(a) {
		t.Fatal("customers should be editable")
	}
	p := &tv.Data.Pending

	// Enter keeps the value; Escape discards it; Ctrl+0 sets NULL.
	typeCell(t, w, tv, cellRef{Row: 1, Column: "email"}, "b@x", gui.KeyEnter, 0)
	if v, ok := p.Value(1, "email"); !ok || v.Null || v.Text != "b@x" || tv.Data.Edit.Active {
		t.Fatalf("email = %+v, %v (editing %v)", v, ok, tv.Data.Edit.Active)
	}
	typeCell(t, w, tv, cellRef{Row: 0, Column: "name"}, "zzz", gui.KeyEscape, 0)
	if _, ok := p.Value(0, "name"); ok {
		t.Fatal("Escape kept the value")
	}
	typeCell(t, w, tv, cellRef{Row: 0, Column: "email"}, "", gui.Key0, gui.ModCtrl)
	if v, _ := p.Value(0, "email"); !v.Null {
		t.Fatalf("Ctrl+0: email = %+v", v)
	}
	// NOT NULL columns do not take NULL.
	typeCell(t, w, tv, cellRef{Row: 2, Column: "name"}, "", gui.Key0, gui.ModCtrl)
	if _, ok := p.Value(2, "name"); ok {
		t.Fatal("NULL accepted in a NOT NULL column")
	}
	endEdit(tv)

	// Select row 0 and delete it from the bottom bar.
	typeCell(t, w, tv, cellRef{Row: 0, Column: "name"}, "", gui.KeyEscape, 0)
	soft.RenderToImage(w, 1)
	clickDialog(t, w, "data-delete")
	if !p.Deleted(0) {
		t.Fatal("row 0 not deleted")
	}
	addRow(w, tv)
	w.FrameFn()
	w.TestRender(nil)
	if e := tv.Data.Edit; !e.Active || !e.Cell.New || e.Cell.Column != "name" {
		t.Fatalf("new row edit = %+v", e)
	}
	if err := w.TestType(editID, "Dora"); err != nil {
		t.Fatal(err)
	}
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "edit-pending")
	if err := w.TestKey(editID, gui.KeyEnter, 0); err != nil {
		t.Fatal(err)
	}
	if p.Len() != 3 {
		t.Fatalf("changes = %+v", p.Changes())
	}

	// Save: preview, then one transaction. Ana has orders, so the DELETE
	// fails, everything is rolled back and the changes stay pending.
	save := func() {
		t.Helper()
		w.TestRender(nil)
		if err := w.TestClick(w.ResolveID("data-save")[0]); err != nil {
			t.Fatal(err)
		}
		clickDialog(t, w, "save-confirm")
		waitFor(t, w, func() bool { return p.Len() == 0 && !tv.Data.Loading || tv.Data.SaveErr != "" })
	}
	save()
	screenshot(t, w, "edit-save-error")
	if !strings.Contains(tv.Data.SaveErr, "DELETE of row 1") || p.Len() != 3 {
		t.Fatalf("save error = %q, changes = %d", tv.Data.SaveErr, p.Len())
	}
	w.DialogDismiss()

	// Delete Carla instead (no orders).
	toggleDelete(w, tv, 0, false)
	toggleDelete(w, tv, 2, false)
	save()
	if tv.Data.SaveErr != "" {
		t.Fatal(tv.Data.SaveErr)
	}
	var got []string
	for _, r := range tv.Data.Page.Rows {
		got = append(got, strings.Join(query.Values(r[:3]), "|")+map[bool]string{true: "(null)"}[r[2].Null])
	}
	// Deleting Ana dropped her edit (restoring a row does not bring it
	// back); changes run in order, so Dora is inserted before Carla goes.
	want := []string{"1|Ana|ana@example.com", "2|Bruno|b@x", "4|Dora|(null)"}
	if !slices.Equal(got, want) {
		t.Fatalf("rows after save = %v", got)
	}

	// Opening another table keeps the pending changes in their tab (it is
	// not replaced as a preview); closing that tab asks first.
	typeCell(t, w, tv, cellRef{Row: 0, Column: "name"}, "!", gui.KeyEnter, 0)
	edited := a.Active
	openTable(w, key, a.Tables[key][2], false) // orders
	if a.table() == tv || a.table().Table.Name != "orders" || !edited.dirty() {
		t.Fatalf("table = %+v, edited tab dirty = %v", a.table().Table, edited.dirty())
	}
	closeTab(w, edited)
	if !slices.Contains(a.Tabs, edited) {
		t.Fatal("closed the tab without asking")
	}
	clickDialog(t, w, "unsaved-discard")
	if slices.Contains(a.Tabs, edited) || a.table().Table.Name != "orders" {
		t.Fatalf("tabs = %d, table = %+v", len(a.Tabs), a.table().Table)
	}
}

func TestMultiSelect(t *testing.T) {
	a := testApp(t)
	w := newTestWindow(t, a)
	key := a.table().Key
	openTable(w, key, a.Tables[key][1], false) // customers
	waitFor(t, w, func() bool { return a.table().Data.fetched })
	tv := a.table()
	p := &tv.Data.Pending
	id := p.AddRow()
	k := func(r int) rowKey { return rowKey{Row: r} }
	sel := func() []rowKey { return tv.selectedRows() }

	// Click, Ctrl+click and Shift+click (grid order: new row, then 0..2).
	selectRow(tv, k(0), "email", 0)
	selectRow(tv, k(2), "email", gui.ModCtrl)
	if got := sel(); !slices.Equal(got, []rowKey{k(0), k(2)}) {
		t.Fatalf("ctrl: %v", got)
	}
	selectRow(tv, k(0), "email", gui.ModCtrl) // toggles it off; 0 is the anchor
	selectRow(tv, k(2), "email", gui.ModShift)
	if got := sel(); !slices.Equal(got, []rowKey{k(0), k(1), k(2)}) {
		t.Fatalf("shift: %v", got)
	}
	// Shift keeps the anchor: from 0 up to the new row.
	selectRow(tv, rowKey{Row: id, New: true}, "email", gui.ModShift)
	if got := sel(); !slices.Equal(got, []rowKey{{Row: id, New: true}, k(0)}) {
		t.Fatalf("shift up: %v", got)
	}
	selectRow(tv, rowKey{Row: id, New: true}, "email", 0)
	selectRow(tv, k(2), "email", gui.ModShift)
	want := []rowKey{{Row: id, New: true}, k(0), k(1), k(2)}
	if got := sel(); !slices.Equal(got, want) {
		t.Fatalf("shift: %v", got)
	}

	// Set NULL applies to the active column of every selected row.
	w.TestRender(nil)
	soft.RenderToImage(w, 1)
	w.TestRender(nil)
	clickDialog(t, w, "data-set-null")
	for r := range 3 {
		if v, _ := p.Value(r, "email"); r != 1 && !v.Null {
			t.Fatalf("row %d email = %+v", r, v)
		}
	}
	if v, ok := p.NewValue(id, "email"); !ok || !v.Null {
		t.Fatalf("new row email = %+v", v)
	}

	// Delete (N): the new row is dropped, the others deleted; again restores.
	selectRow(tv, k(1), "", 0)
	selectRow(tv, k(2), "", gui.ModShift)
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "multi-select")
	clickDialog(t, w, "data-delete")
	if !p.Deleted(1) || !p.Deleted(2) || p.Deleted(0) || !tv.allDeleted() {
		t.Fatalf("changes = %+v", p.Changes())
	}
	clickDialog(t, w, "data-delete")
	if p.Deleted(1) || p.Deleted(2) {
		t.Fatal("Restore did not restore")
	}
	selectRow(tv, rowKey{Row: id, New: true}, "", 0)
	clickDialog(t, w, "data-delete")
	if len(p.NewRows()) != 0 || len(sel()) != 0 {
		t.Fatalf("new rows = %v, selection = %v", p.NewRows(), sel())
	}
}

func TestDateEditor(t *testing.T) {
	a := testApp(t)
	w := newTestWindow(t, a)
	key := a.table().Key
	openTable(w, key, a.Tables[key][1], false) // customers
	waitFor(t, w, func() bool { return a.table().Data.fetched })
	tv := a.table()
	p := &tv.Data.Pending

	// A timestamp opens the calendar on the value's month, with its time.
	ref := cellRef{Row: 0, Column: "created"}
	startEdit(w, tv, ref)
	w.TestRender(nil)
	if !w.DialogIsVisible() || tv.Data.Edit.Active {
		t.Fatal("the date editor did not open")
	}
	de := &tv.Data.Date
	if de.Clock != "10:20:30" || de.Moment.Day() != 1 || de.Moment.Month() != 3 {
		t.Fatalf("editor = %+v", de)
	}
	soft.RenderToImage(w, 1)
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "date-editor")

	// Pick the 15th, change the time and accept.
	day := gui.ScopeIDN(calendarID, "day", 15)
	if ids := w.ResolveID(day); len(ids) == 1 {
		day = ids[0]
	}
	if err := w.TestClick(day); err != nil {
		t.Fatal(err)
	}
	de.Clock = "08:05:00"
	clickDialog(t, w, "date-ok")
	if v, _ := p.Value(0, "created"); v.Text != "2024-03-15 08:05:00" || w.DialogIsVisible() {
		t.Fatalf("created = %+v", v)
	}

	// A bad time keeps the editor open with an error.
	startEdit(w, tv, ref)
	de.Clock = "25:00:00"
	clickDialog(t, w, "date-ok")
	if de.Err == "" || !w.DialogIsVisible() {
		t.Fatal("invalid time accepted")
	}
	clickDialog(t, w, "date-null")
	if v, _ := p.Value(0, "created"); !v.Null {
		t.Fatalf("Set NULL in the editor: %+v", v)
	}

	// Set Now from the bottom bar on the selected cell.
	w.TestRender(nil)
	clickDialog(t, w, "data-set-now")
	v, _ := p.Value(0, "created")
	if _, err := time.Parse("2006-01-02 15:04:05", v.Text); err != nil || v.Null {
		t.Fatalf("Set Now: %+v", v)
	}
	// Set Now is only for dates; Set NULL only for nullable columns.
	typeCell(t, w, tv, cellRef{Row: 1, Column: "name"}, "", gui.KeyEscape, 0)
	if tv.canSetNow() || tv.canSetNull() { // name is a NOT NULL text column
		t.Fatalf("name: canSetNow=%v canSetNull=%v", tv.canSetNow(), tv.canSetNull())
	}
	w.TestRender(nil)
	screenshot(t, w, "edit-bar")
}

func TestCloseWithUnsavedChanges(t *testing.T) {
	a := testApp(t)
	w := newTestWindow(t, a)
	key := a.table().Key
	openTable(w, key, a.Tables[key][1], false) // customers
	waitFor(t, w, func() bool { return a.table().Data.fetched })
	tv := a.table()

	// Nothing pending: it closes right away.
	confirmClose(w)
	if !w.CloseRequested() {
		t.Fatal("did not close without changes")
	}

	w = newTestWindow(t, a)
	tv.Data.Pending.Set(0, "name", editing.Text("Ann"), editing.Text("Ana"))
	confirmClose(w)
	w.TestRender(nil)
	if w.CloseRequested() || !w.DialogIsVisible() {
		t.Fatal("closed with unsaved changes")
	}
	checkFindings(t, w)
	screenshot(t, w, "close-unsaved")

	clickDialog(t, w, "close-keep")
	if w.CloseRequested() || w.DialogIsVisible() || tv.Data.Pending.Len() != 1 {
		t.Fatal("Keep editing did not keep the changes")
	}

	confirmClose(w)
	clickDialog(t, w, "close-discard")
	if !w.CloseRequested() {
		t.Fatal("Discard and close did not close")
	}
}

func TestSQLRun(t *testing.T) {
	a := testApp(t)
	w := newTestWindow(t, a)
	key := a.table().Key
	ed := openSQLTab(w, a, key)
	w.TestRender(nil)

	// The Ctrl+Enter path (a go-edit action) needs a text backend;
	// here the statement under the cursor and runSQL are tested.
	text := "select name, email from customers order by id;\nselect 1 as x;"
	ed.Buf = buffer.FromBytes([]byte(text))
	ed.hl = nil
	if got := statementAt(a, key, text, textOffset(text, buffer.Position{Line: 0, ByteCol: 3})); got != "select name, email from customers order by id" {
		t.Fatalf("statement under cursor = %q", got)
	}
	if got := statementAt(a, key, text, textOffset(text, buffer.Position{Line: 1, ByteCol: 2})); got != "select 1 as x" {
		t.Fatalf("statement on line 2 = %q", got)
	}
	runSQL(w, ed, "select name, email from customers order by id")
	waitFor(t, w, func() bool { return ed.Run != nil })
	w.TestRender(nil)
	if ed.Err != "" || ed.Page == nil || len(ed.Page.Columns) != 2 || len(ed.Page.Rows) != 3 {
		t.Fatalf("result = %+v err=%q", ed.Page, ed.Err)
	}
	screenshot(t, w, "sql-result")

	// A long result is paginated and the grid scrolls inside its panel.
	runSQL(w, ed, "with recursive n(i) as (select 1 union all select i + 1 from n where i < 300) select i, 'row ' || i as label, null as empty from n")
	waitFor(t, w, func() bool { return !ed.Running && (ed.Err != "" || ed.Page != nil && len(ed.Page.Rows) == 100) })
	if ed.Err != "" {
		t.Fatalf("err = %s", ed.Err)
	}
	if !ed.Page.HasNext {
		t.Fatal("expected more pages")
	}
	loadResultPage(w, ed, 2)
	waitFor(t, w, func() bool { return ed.Page.Index == 2 })
	if got := ed.Page.Rows[0][0].Text; got != "201" || ed.Page.HasNext {
		t.Fatalf("page 3 starts at %q, next=%v", got, ed.Page.HasNext)
	}
	w.TestRender(nil)
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "sql-many-rows")

	var entries []models.QueryHistory
	waitFor(t, w, func() bool {
		entries, _ = a.History.List(context.Background(), key.ConnID, "", 10)
		return len(entries) == 2
	})
	if entries[1].SQL != "select name, email from customers order by id" {
		t.Fatalf("history = %q", entries[1].SQL)
	}

	// DELETE without WHERE asks for confirmation before running.
	runSQL(w, ed, "delete from orders")
	w.TestRender(nil)
	if !w.DialogIsVisible() {
		t.Fatal("expected confirmation dialog")
	}
	screenshot(t, w, "sql-warning")
	w.DialogDismiss()

	// Engine errors are shown in the results panel.
	runSQL(w, ed, "select * from nope")
	waitFor(t, w, func() bool { return !ed.Running && ed.Err != "" })
	if !strings.Contains(ed.Err, "no such table") {
		t.Fatalf("err = %q", ed.Err)
	}
}

// rowHeight returns the height of the first data row of table id.
func rowHeight(ly *gui.Layout, id string) float32 {
	if ly == nil || ly.Shape == nil {
		return -1
	}
	if ly.Shape.ID == id+":table:scroll" && len(ly.Children) > 0 {
		return ly.Children[0].Shape.Height
	}
	for i := range ly.Children {
		if h := rowHeight(&ly.Children[i], id); h >= 0 {
			return h
		}
	}
	return -1
}

// Regression: inside nested splitters gui.Table doubled the row height
// (go-gui v0.92); the SQL tab does not use a splitter of its own.
func TestSQLGridRowHeight(t *testing.T) {
	a := testApp(t)
	w := newTestWindow(t, a)
	soft.RenderToImage(w, 1) // text measurer, as in the app
	key := a.table().Key
	ed := openSQLTab(w, a, key)
	runSQL(w, ed, "select name, email from customers order by id")
	waitFor(t, w, func() bool { return ed.Run != nil })
	w.TestRender(nil)
	ly := w.TestRender(nil)
	if h := rowHeight(ly, "sql-grid"); h <= 0 || h > 30 {
		t.Fatalf("row height = %.0f", h)
	}
}

func TestWideTable(t *testing.T) {
	a := testApp(t)
	w := newTestWindow(t, a)
	key := a.table().Key
	var wide drivers.Table
	for _, tb := range a.Tables[key] {
		if tb.Name == "wide" {
			wide = tb
		}
	}
	openTable(w, key, wide, false)
	waitFor(t, w, func() bool { return a.table().Data.fetched })
	w.TestRender(nil)
	w.TestRender(nil)
	checkFindings(t, w)
	screenshot(t, w, "wide")

	// Columns that do not fit are reachable with horizontal scroll up to the
	// last one (go-gui clipped the scroll to a stale content width).
	soft.RenderToImage(w, 1) // text measurer, as in the app
	w.TestRender(nil)
	w.TestRender(nil)
	id := w.ResolveID("data-grid")[0]
	if err := w.TestScroll(id, -1000, 0); err != nil {
		t.Fatal(err)
	}
	w.ScrollHorizontalTo(id, -1e6)
	w.TestRender(nil)
	size := a.table().Data.gridSize
	if x, _, _ := w.TestScrollOffset(id); size.ContentW <= size.W || x > size.W-size.ContentW+1 {
		t.Fatalf("offset %v cannot reach the last column (content %v, viewport %v)", x, size.ContentW, size.W)
	}
	screenshot(t, w, "wide-scrolled")

	// With a narrow table the minimum width shrinks again.
	openTable(w, key, a.Tables[key][1], false) // customers
	waitFor(t, w, func() bool { return a.table().Data.fetched })
	w.TestRender(nil)
	w.TestRender(nil)
	if cw := a.table().Data.gridSize.ContentW; cw == 0 || cw >= size.W {
		t.Fatalf("content width after a narrow table = %v", cw)
	}
}

func TestColumnCache(t *testing.T) {
	a := testApp(t)
	w := newTestWindow(t, a)
	key := dbKey{ConnID: a.Selected}

	loadColumns(w, key, completion.Mentioned("select * from Customers", a.Tables[key]))
	waitFor(t, w, func() bool { return len(a.Columns[key]["customers"]) > 0 })
	if got := a.Columns[key]["customers"][1].Name; got != "name" {
		t.Errorf("second column = %q, want name", got)
	}
	if _, ok := a.Columns[key]["orders"]; ok {
		t.Error("loaded the columns of a table not mentioned")
	}

	refreshConn(w, key.ConnID)
	waitFor(t, w, func() bool { return !a.Loading[key] })
	if _, ok := a.Columns[key]; ok {
		t.Error("refresh kept the cached columns")
	}
}
