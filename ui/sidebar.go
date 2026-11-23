package ui

import (
	"strings"

	"github.com/Watts-Argentina/anvil/internal/drivers"
	"github.com/Watts-Argentina/anvil/models"
	"github.com/Watts-Argentina/anvil/ui/components"
	"github.com/go-gui-org/go-gui/gui"
)

func engineLabel(engine string) string {
	switch engine {
	case models.EngineSQLite:
		return "SQLite"
	case models.EnginePostgres:
		return "PostgreSQL"
	case models.EngineMySQL:
		return "MySQL"
	case models.EngineMSSQL:
		return "SQL Server"
	case models.EngineOracle:
		return "Oracle"
	case models.EngineMongo:
		return "MongoDB"
	}
	return engine
}

func sidebar(w *gui.Window, a *App) gui.View {
	t := gui.CurrentTheme()

	content := make([]gui.View, 0, len(a.Connections))
	for _, c := range a.Connections {
		content = append(content, connectionItem(a, c))
	}
	if len(a.Connections) == 0 {
		content = append(content, gui.Text(gui.TextCfg{
			Text:      "No connections yet. Use Add to create one.",
			Mode:      gui.TextModeWrap,
			TextStyle: t.TextStyleSecondary,
		}))
	}

	return gui.Column(gui.ContainerCfg{
		ID:         "sidebar",
		Sizing:     gui.FillFill,
		Scrollable: true,
		ScrollMode: gui.ScrollVerticalOnly,
		Padding:    gui.PaddingSmall,
		Spacing:    gui.SpacingTight,
		SizeBorder: gui.NoBorder,
		Color:      t.ColorPanel,
		Content:    content,
	})
}

func connectionItem(a *App, c models.DBConn) gui.View {
	t := gui.CurrentTheme()
	id := c.ID
	s, open := a.Sessions.Get(id)
	expanded := open && a.Expanded[id]
	scope := gui.ScopeIDN("conn", "", int(id))

	nameStyle := t.TextStyleDef
	if !open {
		nameStyle = t.TextStyleSecondary
	}

	header := components.TreeRow(gui.ScopeID(scope, "row"), 0, a.Selected == id,
		func(ctx gui.EventCtx) {
			a := appOf(ctx.Window)
			if a.Selected == id {
				if _, open := a.Sessions.Get(id); open {
					a.Expanded[id] = !a.Expanded[id]
				} else {
					connect(ctx.Window, id)
				}
			}
			a.Selected = id
			ctx.Consume()
		},
		connRowContent(a, c, open, nameStyle)...,
	)

	if !expanded {
		return header
	}

	items := []gui.View{header}
	if !s.AllDatabases() {
		items = append(items, tableList(a, c, dbKey{ConnID: id}, scope, 1)...)
		return components.TreeColumn(items)
	}

	// shortcut: no create forms for MongoDB (its databases and collections
	// appear on the first insert); add them if users ask.
	if c.Engine != models.EngineMongo {
		items = append(items, components.TreeIndented(1, components.IconButton("plus", "New database", gui.ButtonCfg{
			ID:       gui.ScopeID(scope, "new-db"),
			Variant:  gui.ButtonGhost,
			Disabled: c.ReadOnly,
			OnClick: func(ctx gui.EventCtx) {
				openNewDatabase(ctx.Window, id)
				ctx.Consume()
			},
		})))
	}
	dbs := a.Databases[id]
	if len(dbs) == 0 {
		items = append(items, components.TreeHint("No databases.", 1))
	}
	for n, name := range dbs {
		items = append(items, databaseItem(a, c, name, gui.ScopeIDN(scope, "db", n))...)
	}
	return components.TreeColumn(items)
}

// connRowContent is the connection row: chevron, color, name, engine and,
// if it is open, the refresh button.
func connRowContent(a *App, c models.DBConn, open bool, nameStyle gui.TextStyle) []gui.View {
	t := gui.CurrentTheme()
	id := c.ID
	content := []gui.View{
		components.Chevron(open && a.Expanded[id]),
		components.ColorDot(c.Color, 10),
		components.TreeLabel(c.Name, nameStyle),
		gui.Text(gui.TextCfg{Text: engineLabel(c.Engine), TextStyle: t.TextStyleCaption}),
	}
	if !open {
		return content
	}
	return append(content, refreshButton(gui.ScopeID(gui.ScopeIDN("conn", "", int(id)), "refresh"),
		"Refresh "+c.Name, a.Connecting[id] || a.Loading[dbKey{ConnID: id}],
		func(w *gui.Window) { refreshConn(w, id) }))
}

// refreshButton is the small reload button of a sidebar row.
func refreshButton(id, label string, busy bool, onClick func(w *gui.Window)) gui.View {
	return components.IconButton("refresh", "", gui.ButtonCfg{
		ID:       id,
		Variant:  gui.ButtonGhost,
		Padding:  gui.PadAll(2),
		Disabled: busy,
		A11YCfg:  gui.A11YCfg{A11YLabel: label},
		OnClick: func(ctx gui.EventCtx) {
			onClick(ctx.Window)
			ctx.Consume()
		},
	})
}

// databaseItem is a server database and, if expanded, its tables.
func databaseItem(a *App, c models.DBConn, name, scope string) []gui.View {
	t := gui.CurrentTheme()
	key := dbKey{ConnID: c.ID, Database: name}
	expanded := a.DBExpanded[key]

	row := []gui.View{
		components.Chevron(expanded),
		components.Icon("database", 12),
		components.TreeLabel(name, t.TextStyleBodySmall),
	}
	if _, loaded := a.Tables[key]; loaded {
		row = append(row, refreshButton(gui.ScopeID(scope, "refresh"), "Refresh "+name, a.Loading[key],
			func(w *gui.Window) { loadTables(w, key, nil) }))
	}
	items := []gui.View{components.TreeRow(gui.ScopeID(scope, "row"), 1, false,
		func(ctx gui.EventCtx) {
			appOf(ctx.Window).Selected = key.ConnID
			toggleDatabase(ctx.Window, key)
			ctx.Consume()
		},
		row...,
	)}
	if !expanded {
		return items
	}
	if a.Loading[key] {
		return append(items, components.TreeHint("Loading tables…", 2))
	}
	return append(items, tableList(a, c, key, scope, 2)...)
}

// tableList is the filter and flat table list of a database.
func tableList(a *App, c models.DBConn, key dbKey, scope string, level int) []gui.View {
	noun := "tables"
	if c.Engine == models.EngineMongo {
		noun = "collections"
	}
	header := []gui.View{
		gui.Input(gui.InputCfg{
			ID:          gui.ScopeID(scope, "filter"),
			Sizing:      gui.FillFit,
			Text:        a.Filter[key],
			Placeholder: "Filter " + noun + "…",
			OnTextChanged: func(text string, ctx gui.EventCtx) {
				appOf(ctx.Window).Filter[key] = text
			},
		}),
	}
	if c.Engine != models.EngineMongo {
		header = append(header, components.IconButton("plus", "", gui.ButtonCfg{
			ID:       gui.ScopeID(scope, "new-table"),
			Variant:  gui.ButtonGhost,
			Disabled: c.ReadOnly,
			A11YCfg:  gui.A11YCfg{A11YLabel: "New table"},
			OnClick: func(ctx gui.EventCtx) {
				openNewTable(ctx.Window, key)
				ctx.Consume()
			},
		}))
	}
	items := []gui.View{
		components.TreeIndented(level, gui.Row(gui.ContainerCfg{
			Sizing:     gui.FillFit,
			Padding:    gui.NoPadding,
			Spacing:    gui.SpacingTight,
			SizeBorder: gui.NoBorder,
			VAlign:     gui.VAlignMiddle,
			Content:    header,
		})),
	}

	filter := strings.ToLower(a.Filter[key])
	shown := 0
	for _, tb := range a.Tables[key] {
		if filter != "" && !strings.Contains(strings.ToLower(tb.Name), filter) {
			continue
		}
		items = append(items, tableItem(a, c, key, tb, gui.ScopeIDN(scope, "table", shown), level))
		shown++
	}
	if shown == 0 {
		msg := "No " + noun + "."
		if filter != "" {
			msg = "No " + noun + " match the filter."
		}
		items = append(items, components.TreeHint(msg, level))
	}
	return items
}

func tableItem(a *App, c models.DBConn, key dbKey, tb drivers.Table, id string, level int) gui.View {
	t := gui.CurrentTheme()
	label := tb.Name
	// In MySQL the schema is the database, so it is only shown in PG outside public.
	if tb.Schema != "" && tb.Schema != "public" && tb.Schema != key.Database && tb.Schema != c.DB {
		label = tb.Schema + "." + tb.Name
	}
	tv := a.table()
	selected := tv != nil && tv.Key == key && tv.Table == tb

	return components.TreeRow(id, level, selected,
		func(ctx gui.EventCtx) {
			// A click opens the preview tab; a double click pins it.
			openTable(ctx.Window, key, tb, appOf(ctx.Window).doubleClick(id))
			ctx.Consume()
		},
		components.Icon(tableIcon(tb), 12),
		components.TreeLabel(label, t.TextStyleBodySmall),
	)
}
