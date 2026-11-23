package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Watts-Argentina/anvil/internal/drivers"
	"github.com/Watts-Argentina/anvil/internal/editing"
	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/models"
	"github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend/soft"
)

// overflows lists the shapes that stick out of an ancestor that does not
// scroll or clip on that axis. The text measurer must be installed
// (soft.RenderToImage) so sizes are the real ones.
func overflows(ly *gui.Layout) []string {
	var out []string
	var walk func(n *gui.Layout, boundX, boundY *gui.Shape)
	walk = func(n *gui.Layout, boundX, boundY *gui.Shape) {
		s := n.Shape
		if s == nil {
			return
		}
		if b := boundX; b != nil && s.Width > 0 && (s.X < b.X-0.5 || s.X+s.Width > b.X+b.Width+0.5) {
			out = append(out, fmt.Sprintf("%q x [%.0f, %.0f] outside %q [%.0f, %.0f]",
				s.ID, s.X, s.X+s.Width, b.ID, b.X, b.X+b.Width))
			return
		}
		if b := boundY; b != nil && s.Height > 0 && (s.Y < b.Y-0.5 || s.Y+s.Height > b.Y+b.Height+0.5) {
			out = append(out, fmt.Sprintf("%q y [%.0f, %.0f] outside %q [%.0f, %.0f]",
				s.ID, s.Y, s.Y+s.Height, b.ID, b.Y, b.Y+b.Height))
			return
		}
		if s.Width > 0 && s.Height > 0 {
			boundX, boundY = s, s
		}
		// Content that scrolls or is clipped (an input's text) may be larger.
		if s.Clip && !s.Scrollable {
			boundX, boundY = nil, nil
		}
		if s.Scrollable {
			if s.ScrollMode != gui.ScrollVerticalOnly {
				boundX = nil
			}
			if s.ScrollMode != gui.ScrollHorizontalOnly {
				boundY = nil
			}
		}
		for i := range n.Children {
			walk(&n.Children[i], boundX, boundY)
		}
	}
	walk(ly, nil, nil)
	return out
}

func checkOverflow(t *testing.T, w *gui.Window, name string) {
	t.Helper()
	soft.RenderToImage(w, 1)
	w.FrameFn()
	if found := overflows(w.TestRender(nil)); len(found) > 0 {
		t.Errorf("%s overflows:\n%v", name, found)
	}
	screenshot(t, w, "overflow-"+name)
}

// TestNoOverflow opens every dialog, with long values where they can be
// long, and checks that nothing sticks out of it.
func TestNoOverflow(t *testing.T) {
	long := strings.Repeat("very_long_name_", 8)
	cases := []struct {
		name string
		open func(w *gui.Window, a *App)
	}{
		{"main", func(w *gui.Window, a *App) {}},
		{"sidebar-long-names", func(w *gui.Window, a *App) {
			a.Connections[0].Name = long
			a.Tables[a.table().Key] = append(a.Tables[a.table().Key], drivers.Table{Name: long})
		}},
		{"many-tabs", func(w *gui.Window, a *App) {
			key := a.table().Key
			for i := range 12 {
				a.newTab(&tab{Key: key, Sub: tabStructure, Table: &tableView{Key: key, Table: drivers.Table{Name: long + itoa(i)}}})
			}
			newSQLTab(w, key)
			// go-edit lays out a floating layer past the editor, so a table
			// tab stays active.
			activateTab(w, a.Tabs[1])
		}},
		{"conn-form-pg", func(w *gui.Window, a *App) { openConnForm(w, models.DBConn{}) }},
		{"conn-form-sqlite", func(w *gui.Window, a *App) {
			c, _ := a.conn(a.Selected)
			c.DB = "/home/user/" + long + "/" + long + ".db"
			openConnForm(w, c)
			a.Form.Error = "Failed to connect. " + strings.Repeat("the server said something long ", 8)
		}},
		{"password", func(w *gui.Window, a *App) {
			promptPassword(w, models.DBConn{Name: long, User: long, Host: long},
				"Failed to connect. "+strings.Repeat("password authentication failed ", 6))
		}},
		{"delete", func(w *gui.Window, a *App) { askDelete(w, models.DBConn{Name: long}) }},
		{"export", func(w *gui.Window, a *App) {
			openExport(w, long, query.Result{}, nil)
			a.Export.Error = "Export failed. " + strings.Repeat("could not write the file ", 8)
		}},
		{"error", func(w *gui.Window, a *App) { showError(w, errors.New(strings.Repeat("error text ", 40))) }},
		{"new-table", func(w *gui.Window, a *App) {
			openNewTable(w, a.table().Key)
			a.NewTable.Name = long
			a.NewTable.Columns[1].Name = long
			a.NewTable.Columns[1].Default = "'" + long + "'"
		}},
		{"new-table-many-columns", func(w *gui.Window, a *App) {
			openNewTable(w, a.table().Key)
			for range 30 {
				a.NewTable.addColumn(schemaColumn("c", "text", ""))
			}
		}},
		{"new-table-error", func(w *gui.Window, a *App) {
			openNewTable(w, a.table().Key)
			a.NewTable.Error = "The table was not created. " + strings.Repeat("the server rejected it ", 8)
		}},
		{"new-database", func(w *gui.Window, a *App) {
			openNewDatabase(w, a.Selected)
			a.NewDB.Name = long + long
			a.NewDB.Error = "The database was not created. " + long
		}},
		{"save-changes", func(w *gui.Window, a *App) {
			tv := a.table()
			tv.Data.Pending.Set(0, "name", editing.Text(long+long), editing.Text("Ana"))
			reviewSave(w, tv, nil)
		}},
		{"leave", func(w *gui.Window, a *App) {
			a.table().Data.Pending.Set(0, "name", editing.Text("Ann"), editing.Text("Ana"))
			leaveTable(w, a.table(), true, func(*gui.Window) {})
		}},
		{"close", func(w *gui.Window, a *App) {
			a.table().Data.Pending.Set(0, "name", editing.Text("Ann"), editing.Text("Ana"))
			confirmClose(w)
		}},
		{"date-editor", func(w *gui.Window, a *App) {
			startEdit(w, a.table(), cellRef{Row: 0, Column: "created"})
		}},
	}
	for _, theme := range []string{models.ThemeDark, models.ThemeLight} {
		for _, tc := range cases {
			t.Run(theme+"/"+tc.name, func(t *testing.T) {
				applyTheme(nil, theme)
				t.Cleanup(func() { applyTheme(nil, models.ThemeDark) })
				a := testApp(t)
				w := newTestWindow(t, a)
				key := a.table().Key
				openTable(w, key, a.Tables[key][1], false) // customers
				waitFor(t, w, func() bool { return a.table().Data.fetched })
				tc.open(w, a)
				checkOverflow(t, w, theme+"-"+tc.name)
			})
		}
	}
}
