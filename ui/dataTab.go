package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Watts-Argentina/anvil/internal/editing"
	"github.com/Watts-Argentina/anvil/internal/mongodb"
	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/internal/session"
	"github.com/Watts-Argentina/anvil/models"
	"github.com/Watts-Argentina/anvil/ui/components"
	"github.com/go-gui-org/go-gui/gui"
)

// dataState is a table's Data tab: a paginated grid with pending
// changes (SPEC §6, §7).
type dataState struct {
	Page     query.Page
	Size     int
	Loading  bool
	Err      string
	Count    string // empty until requested
	Counting bool
	fetched  bool
	gridSize components.BoxSize

	// Pending changes refer to the rows of Page: changing page asks to save
	// or discard them first.
	Pending editing.Pending
	Edit    cellEdit
	Sel     selection
	Date    dateEdit
	NoPKOK  bool // the no-PK warning was accepted

	// MongoDB: AsTable shows the grid instead of the Documents view, and
	// Open holds the objects and arrays expanded in it (per page).
	AsTable bool
	Open    map[docKey]bool
	Saving  bool
	SaveErr string
}

func (d dataState) loaded() bool { return d.fetched || d.Loading }

const firstPage = query.First

var pageSizes = []string{"20", "50", "100", "200", "500"}

func itoa(n int) string { return strconv.Itoa(n) }

// tableSource builds the table source with its database's pool.
func tableSource(ctx context.Context, a *App, tv *tableView) (query.TableSource, error) {
	s, ok := a.Sessions.Get(tv.Key.ConnID)
	if !ok {
		return query.TableSource{}, fmt.Errorf("connection closed")
	}
	db, err := s.Pool(ctx, tv.Key.Database)
	if err != nil {
		return query.TableSource{}, err
	}
	return query.TableSource{Driver: s.Driver, DB: db, Table: tv.Table, Columns: tv.Columns, Filter: tv.Filter}, nil
}

// mongoSession returns the session of connection id if it is an open
// MongoDB one, or nil.
func mongoSession(a *App, id uint) *session.Session {
	if s, ok := a.Sessions.Get(id); ok && s.IsMongo() {
		return s
	}
	return nil
}

func loadData(w *gui.Window, tv *tableView, dir query.Dir) {
	a := appOf(w)
	if tv.Data.Loading {
		return
	}
	tv.Data.Loading, tv.Data.Err = true, ""
	cur, size := tv.Data.Page, tv.Data.Size

	async(w, func(ctx context.Context) (query.Page, error) {
		if s := mongoSession(a, tv.Key.ConnID); s != nil {
			return mongodb.LoadPage(ctx, s.Collection(tv.Key.Database, tv.Table), tv.Columns, cur, dir, size)
		}
		src, err := tableSource(ctx, a, tv)
		if err != nil {
			return query.Page{}, err
		}
		return query.LoadTablePage(ctx, src, cur, dir, size)
	}, func(w *gui.Window, p query.Page, err error) {
		tv.Data.Loading, tv.Data.fetched = false, true
		if err != nil {
			tv.Data.Err = errText(err)
			return
		}
		tv.Data.Page, tv.Data.Open = p, nil
		// Pending changes refer to the rows of the old page.
		discardChanges(tv)
	})
}

func countRows(w *gui.Window, tv *tableView) {
	a := appOf(w)
	tv.Data.Counting = true
	async(w, func(ctx context.Context) (int64, error) {
		if s := mongoSession(a, tv.Key.ConnID); s != nil {
			return mongodb.Count(ctx, s.Collection(tv.Key.Database, tv.Table))
		}
		src, err := tableSource(ctx, a, tv)
		if err != nil {
			return 0, err
		}
		return query.Count(ctx, src)
	}, func(w *gui.Window, n int64, err error) {
		tv.Data.Counting = false
		if err != nil {
			showError(w, err)
			return
		}
		tv.Data.Count = query.FormatCount(n) + " rows"
	})
}

func dataTab(w *gui.Window, a *App, tv *tableView) gui.View {
	t := gui.CurrentTheme()
	d := tv.Data

	status := ""
	switch {
	case d.Loading:
		status = "Loading…"
	case d.fetched:
		first := d.Page.Index*d.Page.Size + 1
		status = fmt.Sprintf("Rows %d–%d", first, first+len(d.Page.Rows)-1)
		if len(d.Page.Rows) == 0 {
			status = "No rows"
		}
	}

	countView := gui.View(gui.Button(gui.ButtonCfg{
		ID:       "data-count",
		Label:    "Count rows",
		Variant:  gui.ButtonGhost,
		Disabled: d.Counting || !d.fetched,
		OnClick: func(ctx gui.EventCtx) {
			countRows(ctx.Window, tv)
			ctx.Consume()
		},
	}))
	if d.Count != "" {
		countView = gui.Text(gui.TextCfg{Text: d.Count, TextStyle: t.TextStyleSecondary})
	}

	if f := filterText(tv.Filter); f != "" {
		status = strings.TrimSuffix(f+" · "+status, " · ")
	}

	toolbar := components.Toolbar("",
		components.IconButton("refresh", "", gui.ButtonCfg{
			ID:       "data-refresh",
			A11YCfg:  gui.A11YCfg{A11YLabel: "Refresh"},
			Disabled: d.Loading,
			OnClick: func(ctx gui.EventCtx) {
				ctx.Consume()
				leaveTable(ctx.Window, tv, false, func(w *gui.Window) {
					tv.Data.Count = ""
					loadData(w, tv, query.Reload)
				})
			},
		}),
		components.IconButton("chevronLeft", "", gui.ButtonCfg{
			ID:       "data-prev",
			A11YCfg:  gui.A11YCfg{A11YLabel: "Previous page"},
			Disabled: d.Loading || d.Page.Index == 0,
			OnClick: func(ctx gui.EventCtx) {
				ctx.Consume()
				leaveTable(ctx.Window, tv, false, func(w *gui.Window) { loadData(w, tv, query.Prev) })
			},
		}),
		gui.Text(gui.TextCfg{Text: "Page " + itoa(d.Page.Index+1), TextStyle: t.TextStyleBodySmall}),
		components.IconButton("chevronRight", "", gui.ButtonCfg{
			ID:       "data-next",
			A11YCfg:  gui.A11YCfg{A11YLabel: "Next page"},
			Disabled: d.Loading || !d.Page.HasNext,
			OnClick: func(ctx gui.EventCtx) {
				ctx.Consume()
				leaveTable(ctx.Window, tv, false, func(w *gui.Window) { loadData(w, tv, query.Next) })
			},
		}),
		gui.Select(gui.SelectCfg{
			ID:       "data-page-size",
			Items:    pageSizes,
			Selected: []string{itoa(d.Size)},
			OnSelect: func(sel []string, ctx gui.EventCtx) {
				ctx.Consume()
				n, err := strconv.Atoi(sel[0])
				if err != nil || n == tv.Data.Size {
					return
				}
				leaveTable(ctx.Window, tv, false, func(w *gui.Window) {
					tv.Data.Size = min(max(n, models.MinPageSize), models.MaxPageSize)
					loadData(w, tv, query.First)
				})
			},
		}),
		gui.Text(gui.TextCfg{Text: "rows per page", TextStyle: t.TextStyleCaption}),
		components.Spacer(),
		gui.Text(gui.TextCfg{Text: status, TextStyle: t.TextStyleCaption}),
		countView,
		exportButton("data-export", d.Loading || !d.fetched || d.Err != "", func(w *gui.Window) {
			openExport(w, tv.Table.Name, tv.Data.Page.Result, func(ctx context.Context, sink query.RowSink) error {
				if s := mongoSession(a, tv.Key.ConnID); s != nil {
					return mongodb.Stream(ctx, s.Collection(tv.Key.Database, tv.Table), tv.Columns, sink)
				}
				src, err := tableSource(ctx, a, tv)
				if err != nil {
					return err
				}
				return query.StreamTable(ctx, src, sink)
			})
		}),
	)

	c, _ := a.conn(tv.Key.ConnID)
	docs := c.Engine == models.EngineMongo
	if docs {
		mode := "Documents"
		if d.AsTable {
			mode = "Table"
		}
		toolbar = gui.Column(gui.ContainerCfg{
			Sizing:     gui.FillFit,
			Padding:    gui.NoPadding,
			Spacing:    gui.SpacingSmall,
			SizeBorder: gui.NoBorder,
			Content: []gui.View{toolbar, gui.SegmentedControl(gui.SegmentedControlCfg{
				ID:      "data-view",
				Items:   []string{"Documents", "Table"},
				Value:   mode,
				A11YCfg: gui.A11YCfg{A11YLabel: "Show documents or a table"},
				OnSelect: func(v string, ctx gui.EventCtx) {
					// The pending changes and the selection are shared by both views.
					if commitEdit(ctx.Window, tv) {
						tv.Data.AsTable = v == "Table"
					}
					ctx.Consume()
				},
			})},
		})
	}

	var body gui.View
	switch {
	case d.Err != "":
		body = components.ErrorText(d.Err)
	case !d.fetched:
		body = gui.Text(gui.TextCfg{Text: "Loading rows…", TextStyle: t.TextStyleSecondary})
	case docs && !d.AsTable:
		body = docList(a, tv)
	default:
		body = dataGrid(a, tv)
	}

	content := []gui.View{toolbar, body}
	if tv.editable(a) && d.fetched && d.Err == "" {
		content = append(content, editBar(tv))
	}

	return gui.Column(gui.ContainerCfg{
		ID:         "data",
		Sizing:     gui.FillFill,
		Padding:    gui.NoPadding,
		Spacing:    gui.SpacingSmall,
		SizeBorder: gui.NoBorder,
		Content:    content,
	})
}

// editBar is the bottom bar with the editing actions (SPEC §7.5):
// Save, Cancel | Add, Delete | Set NULL, Set Now. Delete acts on the
// selected rows; Set NULL and Set Now on the active column of each of them.
func editBar(tv *tableView) gui.View {
	t := gui.CurrentTheme()
	d := &tv.Data
	n := d.Pending.Len()
	selected := len(tv.selectedRows())

	deleteLabel := "Delete"
	if tv.allDeleted() {
		deleteLabel = "Restore"
	}
	if selected > 1 {
		deleteLabel += fmt.Sprintf(" (%d)", selected)
	}
	var notes []string
	if selected > 1 {
		notes = append(notes, itoa(selected)+" selected")
	}
	switch {
	case n > 0:
		notes = append(notes, pendingSummary(&d.Pending))
	case !tv.hasPK():
		notes = append(notes, "No primary key: rows matched by all values")
	case selected <= 1:
		notes = append(notes, "Ctrl/Shift+click selects rows")
	}
	hint := strings.Join(notes, " · ")

	return components.Toolbar("data-edit-bar",
		components.IconButton("save", fmt.Sprintf("Save (%d)", n), gui.ButtonCfg{
			ID:       "data-save",
			Variant:  gui.ButtonPrimary,
			Disabled: n == 0 && !d.Edit.Active || d.Saving,
			OnClick: func(ctx gui.EventCtx) {
				reviewSave(ctx.Window, tv, nil)
				ctx.Consume()
			},
		}),
		components.IconButton("undo", "Cancel", gui.ButtonCfg{
			ID:       "data-discard",
			A11YCfg:  gui.A11YCfg{A11YDescription: "Discard every pending change"},
			Disabled: n == 0 && !d.Edit.Active,
			OnClick: func(ctx gui.EventCtx) {
				discardChanges(tv)
				ctx.Consume()
			},
		}),
		components.Separator(),
		components.IconButton("plus", "Add", gui.ButtonCfg{
			ID:      "data-add-row",
			Variant: gui.ButtonGhost,
			A11YCfg: gui.A11YCfg{A11YDescription: "Add a row"},
			OnClick: func(ctx gui.EventCtx) {
				addRow(ctx.Window, tv)
				ctx.Consume()
			},
		}),
		components.IconButton("trash", deleteLabel, gui.ButtonCfg{
			ID:            "data-delete",
			Variant:       gui.ButtonGhost,
			A11YCfg:       gui.A11YCfg{A11YDescription: deleteLabel + " the selected rows"},
			FocusDisabled: true,
			Disabled:      selected == 0,
			OnClick: func(ctx gui.EventCtx) {
				deleteSelected(ctx.Window, tv)
				ctx.Consume()
			},
		}),
		components.Separator(),
		gui.Button(gui.ButtonCfg{
			ID:            "data-set-null",
			Label:         "Set NULL",
			Variant:       gui.ButtonGhost,
			FocusDisabled: true, // the cell being edited keeps the focus
			Disabled:      !tv.canSetNull(),
			OnClick: func(ctx gui.EventCtx) {
				setNull(ctx.Window, tv)
				ctx.Consume()
			},
		}),
		gui.Button(gui.ButtonCfg{
			ID:            "data-set-now",
			Label:         "Set Now",
			Variant:       gui.ButtonGhost,
			FocusDisabled: true,
			Disabled:      !tv.canSetNow(),
			OnClick: func(ctx gui.EventCtx) {
				setNow(ctx.Window, tv)
				ctx.Consume()
			},
		}),
		// The notes take the space left and are clipped, so they never
		// widen the panel.
		gui.Row(gui.ContainerCfg{
			Sizing:     gui.FillFit,
			Padding:    gui.NoPadding,
			SizeBorder: gui.NoBorder,
			HAlign:     gui.HAlignEnd,
			Clip:       true,
			Content:    []gui.View{gui.Text(gui.TextCfg{Text: hint, TextStyle: t.TextStyleCaption})},
		}),
	)
}

// filterText describes the foreign key filter ("customer_id = 1").
func filterText(filter []query.Cond) string {
	parts := make([]string, len(filter))
	for i, c := range filter {
		parts[i] = c.Column + " = " + query.Text(c.Value)
	}
	return strings.Join(parts, ", ")
}
