package ui

import (
	"slices"
	"time"

	"github.com/Watts-Argentina/anvil/internal/drivers"
	"github.com/Watts-Argentina/anvil/models"
	"github.com/Watts-Argentina/anvil/ui/components"
	"github.com/go-gui-org/go-glyph"
	"github.com/go-gui-org/go-gui/gui"
)

// tab is a tab of the central area (SPEC §4.1): a table, with its
// Structure / Data views, or an SQL editor.
type tab struct {
	ID  int // stable, for widget IDs
	Key dbKey
	// Table is the current view of a table tab (the end of its FK chain);
	// SQL is the editor of an SQL tab. Exactly one of them is set.
	Table *tableView
	SQL   *sqlEditor
	Sub   string // structure or data (table tabs)
	// Preview tabs are replaced by the next table opened from the sidebar.
	Preview bool
}

// preview reports whether opening another table may replace this tab: it
// is a preview and nothing in it would be lost.
func (t *tab) preview() bool {
	return t.Preview && t.Table != nil && pendingView(t.Table) == nil
}

// dirty reports whether the tab has unsaved grid changes.
func (t *tab) dirty() bool { return t.Table != nil && pendingView(t.Table) != nil }

// table returns the table view of the active tab, if it is a table tab.
func (a *App) table() *tableView {
	if a.Active == nil {
		return nil
	}
	return a.Active.Table
}

// newTab adds a tab after the active one and activates it.
func (a *App) newTab(t *tab) *tab {
	a.tabSeq++
	t.ID = a.tabSeq
	i := len(a.Tabs)
	if n := slices.Index(a.Tabs, a.Active); n >= 0 {
		i = n + 1
	}
	a.Tabs = slices.Insert(a.Tabs, i, t)
	a.Active = t
	a.Selected = t.Key.ConnID
	return t
}

// tabOf returns the tab whose current view is the unfiltered table tb.
func (a *App) tabOf(key dbKey, tb drivers.Table) *tab {
	for _, t := range a.Tabs {
		if tv := t.Table; tv != nil && tv.Key == key && tv.Table == tb && tv.Filter == nil {
			return t
		}
	}
	return nil
}

// openTable shows the table from the sidebar: it activates its tab if it is
// open, or else opens it in the preview tab (SPEC §4.1). pin makes the tab
// permanent.
func openTable(w *gui.Window, key dbKey, tb drivers.Table, pin bool) {
	a := appOf(w)
	if t := a.tabOf(key, tb); t != nil {
		activateTab(w, t)
		if pin {
			t.Preview = false
		}
		return
	}
	if !commitActive(w) {
		return
	}
	for _, t := range a.Tabs {
		if !t.preview() {
			continue
		}
		// The FK chain has no pending changes, so nothing is asked.
		a.Active = t
		t.Preview = !pin
		t.Key = key
		showTable(w, t, &tableView{Key: key, Table: tb})
		return
	}
	t := a.newTab(&tab{Key: key, Sub: a.TabSel, Preview: !pin})
	showTable(w, t, &tableView{Key: key, Table: tb})
}

// newSQLTab opens an SQL editor tab on a database.
func newSQLTab(w *gui.Window, key dbKey) {
	a := appOf(w)
	if _, ok := a.Sessions.Get(key.ConnID); !ok || !commitActive(w) {
		return
	}
	a.newTab(&tab{Key: key, SQL: newEditor(a, key)})
}

// sqlTabKey is the database a new SQL tab opens on: the active tab's, or
// the selected connection's when it has its database configured.
func sqlTabKey(a *App) (dbKey, bool) {
	if a.Active != nil {
		return a.Active.Key, true
	}
	if s, ok := a.Sessions.Get(a.Selected); ok && !s.AllDatabases() {
		return dbKey{ConnID: a.Selected}, true
	}
	return dbKey{}, false
}

// commitActive keeps the cell being edited in the active tab. It returns
// false if its value is not valid: the edit stays open.
func commitActive(w *gui.Window) bool {
	tv := appOf(w).table()
	return tv == nil || commitEdit(w, tv)
}

// activateTab switches to t. Its pending changes stay where they are.
func activateTab(w *gui.Window, t *tab) {
	a := appOf(w)
	if a.Active != t {
		if !commitActive(w) {
			return
		}
		a.Active = t
	}
	a.Selected = t.Key.ConnID
	if tv := t.Table; tv != nil && t.Sub == tabData && !tv.Loading && tv.Err == "" && !tv.Data.loaded() {
		loadData(w, tv, firstPage)
	}
}

// cycleTab activates the next (dir 1) or previous (dir -1) tab.
func cycleTab(w *gui.Window, dir int) {
	a := appOf(w)
	if len(a.Tabs) < 2 {
		return
	}
	i := slices.Index(a.Tabs, a.Active)
	activateTab(w, a.Tabs[(i+dir+len(a.Tabs))%len(a.Tabs)])
}

// closeTab closes t after saving or discarding its pending changes.
func closeTab(w *gui.Window, t *tab) {
	leaveTable(w, t.Table, true, func(w *gui.Window) { dropTab(appOf(w), t) })
}

// dropTab removes t without asking and activates its neighbor.
func dropTab(a *App, t *tab) {
	i := slices.Index(a.Tabs, t)
	if i < 0 {
		return
	}
	if t.SQL != nil {
		t.SQL.close()
	}
	a.Tabs = slices.Delete(a.Tabs, i, i+1)
	if a.Active != t {
		return
	}
	a.Active = nil
	if len(a.Tabs) > 0 {
		a.Active = a.Tabs[min(i, len(a.Tabs)-1)]
		a.Selected = a.Active.Key.ConnID
	}
}

// leaveTabs runs then after the user saves or discards the pending changes
// of every tab of connection id (disconnect, delete).
func leaveTabs(w *gui.Window, id uint, then func(w *gui.Window)) {
	for _, t := range appOf(w).Tabs {
		if t.Key.ConnID == id && t.dirty() {
			activateTab(w, t)
			leaveTable(w, t.Table, true, func(w *gui.Window) { leaveTabs(w, id, then) })
			return
		}
	}
	then(w)
}

// doubleClick reports whether this click on id follows another one on the
// same widget closely enough to be a double click (go-gui has no click count).
func (a *App) doubleClick(id string) bool {
	now := time.Now()
	double := a.lastClick.id == id && now.Sub(a.lastClick.at) < 400*time.Millisecond
	a.lastClick.id, a.lastClick.at = id, now
	if double {
		a.lastClick.id = ""
	}
	return double
}

// tabTitle is the text of a tab: the table (with its schema if it is not
// the database's) or the SQL editor's database.
func tabTitle(a *App, t *tab) string {
	if t.SQL != nil {
		if c, _ := a.conn(t.Key.ConnID); c.Engine == models.EngineMongo {
			return "Query · " + dbLabel(a, t.Key)
		}
		return "SQL · " + dbLabel(a, t.Key)
	}
	tv := t.Table
	name := tv.Table.Name
	if tv.Table.Schema != "" && tv.Table.Schema != "public" && tv.Table.Schema != tv.Key.Database {
		if c, ok := a.conn(tv.Key.ConnID); !ok || tv.Table.Schema != c.DB {
			name = tv.Table.Schema + "." + name
		}
	}
	return name
}

// dbLabel names a database: its name, the configured one or, in SQLite,
// the connection.
func dbLabel(a *App, key dbKey) string {
	if key.Database != "" {
		return key.Database
	}
	c, ok := a.conn(key.ConnID)
	if !ok {
		return ""
	}
	if c.Engine == models.EngineSQLite || c.DB == "" {
		return c.Name
	}
	return c.DB
}

// tabBar is the row of open tabs above the central area, with the button
// that opens an SQL editor.
func tabBar(a *App) gui.View {
	items := make([]gui.View, 0, len(a.Tabs))
	for _, t := range a.Tabs {
		items = append(items, tabChip(a, t))
	}
	key, canSQL := sqlTabKey(a)
	th := gui.CurrentTheme()
	return gui.Row(gui.ContainerCfg{
		ID:         "tab-strip",
		Sizing:     gui.FillFit,
		Padding:    gui.PadVH(2, 4),
		Spacing:    gui.SpacingTight,
		SizeBorder: gui.NoBorder,
		Color:      th.ColorPanel,
		VAlign:     gui.VAlignMiddle,
		Content: []gui.View{
			gui.Row(gui.ContainerCfg{
				ID:         "tab-bar",
				Sizing:     gui.FillFit,
				Padding:    gui.NoPadding,
				Spacing:    gui.SpacingTight,
				SizeBorder: gui.NoBorder,
				Scrollable: true,
				ScrollMode: gui.ScrollHorizontalOnly,
				VAlign:     gui.VAlignMiddle,
				Content:    items,
			}),
			components.IconButton("code", "", gui.ButtonCfg{
				ID:       "tab-new-sql",
				Variant:  gui.ButtonGhost,
				Disabled: !canSQL,
				A11YCfg:  gui.A11YCfg{A11YLabel: "New SQL editor", A11YDescription: "Ctrl+T"},
				OnClick: func(ctx gui.EventCtx) {
					newSQLTab(ctx.Window, key)
					ctx.Consume()
				},
			}),
		},
	})
}

// tabChip is one tab: connection color, icon, title, unsaved mark and close
// button. Preview tabs are in italics; a double click pins them.
func tabChip(a *App, t *tab) gui.View {
	th := gui.CurrentTheme()
	scope := gui.ScopeIDN("tab", "", t.ID)
	active := a.Active == t

	icon := "code"
	if t.Table != nil {
		icon = tableIcon(t.Table.Table)
	}
	style := th.TextStyleBodySmall
	if !active {
		style = th.TextStyleSecondary
	}
	if t.preview() {
		style.Typeface = glyph.TypefaceItalic
	}
	c, _ := a.conn(t.Key.ConnID)

	content := []gui.View{
		components.ColorDot(c.Color, 8),
		components.Icon(icon, 12),
		gui.Text(gui.TextCfg{Text: components.Ellipsis(tabTitle(a, t), 32), TextStyle: style}),
	}
	if t.dirty() {
		content = append(content, gui.Text(gui.TextCfg{Text: "•", TextStyle: th.TextStyleWarning}))
	}
	content = append(content, components.IconButton("x", "", gui.ButtonCfg{
		ID:      gui.ScopeID(scope, "close"),
		Variant: gui.ButtonGhost,
		Padding: gui.PadAll(2),
		A11YCfg: gui.A11YCfg{A11YLabel: "Close " + tabTitle(a, t)},
		OnClick: func(ctx gui.EventCtx) {
			closeTab(ctx.Window, t)
			ctx.Consume()
		},
	}))

	color := gui.Color{}
	if active {
		color = th.ColorBackground
	}
	return gui.Row(gui.ContainerCfg{
		ID:         scope,
		Padding:    gui.NewPadding(3, 4, 3, 8),
		Spacing:    gui.SpacingSmall,
		SizeBorder: gui.NoBorder,
		Radius:     gui.RadiusSmall,
		Color:      color,
		VAlign:     gui.VAlignMiddle,
		Hover:      gui.HoverStyle{Color: th.ColorHover, Cursor: gui.CursorPointingHand},
		OnClick: func(ctx gui.EventCtx) {
			a := appOf(ctx.Window)
			activateTab(ctx.Window, t)
			if a.doubleClick(scope) {
				t.Preview = false
			}
			ctx.Consume()
		},
		Content: content,
	})
}

// registerTabCommands binds the tab shortcuts (SPEC §4.1). They are global:
// they work with the focus in the SQL editor or the grid, but not while a
// dialog is open.
func registerTabCommands(w *gui.Window) error {
	noDialog := func(w *gui.Window) bool { return !w.DialogIsVisible() }
	return w.RegisterCommands(
		gui.Command{
			ID: "tab.close", Label: "Close tab", Global: true, CanExecute: noDialog,
			Shortcut: gui.Shortcut{Key: gui.KeyW, Modifiers: gui.ModCtrl},
			Execute: func(_ *gui.Event, w *gui.Window) {
				if t := appOf(w).Active; t != nil {
					closeTab(w, t)
				}
			},
		},
		gui.Command{
			ID: "tab.next", Label: "Next tab", Global: true, CanExecute: noDialog,
			Shortcut: gui.Shortcut{Key: gui.KeyTab, Modifiers: gui.ModCtrl},
			Execute:  func(_ *gui.Event, w *gui.Window) { cycleTab(w, 1) },
		},
		gui.Command{
			ID: "tab.prev", Label: "Previous tab", Global: true, CanExecute: noDialog,
			Shortcut: gui.Shortcut{Key: gui.KeyTab, Modifiers: gui.ModCtrl | gui.ModShift},
			Execute:  func(_ *gui.Event, w *gui.Window) { cycleTab(w, -1) },
		},
		gui.Command{
			ID: "tab.sql", Label: "New SQL editor", Global: true, CanExecute: noDialog,
			Shortcut: gui.Shortcut{Key: gui.KeyT, Modifiers: gui.ModCtrl},
			Execute: func(_ *gui.Event, w *gui.Window) {
				if key, ok := sqlTabKey(appOf(w)); ok {
					newSQLTab(w, key)
				}
			},
		},
	)
}
