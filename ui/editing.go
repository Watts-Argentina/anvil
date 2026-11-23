package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Watts-Argentina/anvil/internal/drivers"
	"github.com/Watts-Argentina/anvil/internal/editing"
	"github.com/Watts-Argentina/anvil/internal/mongodb"
	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/internal/session"
	"github.com/Watts-Argentina/anvil/ui/components"
	"github.com/go-gui-org/go-gui/gui"
)

// cellRef is a grid cell: Row is the page row index, or the new row's ID
// when New is set.
type cellRef struct {
	Row    int
	New    bool
	Column string
}

// cellEdit is the cell being edited in the Data tab. Gen changes every time
// an edit starts or ends, so a commit that arrives late (blur after Escape
// or after moving to another cell) is ignored.
type cellEdit struct {
	Active bool
	Cell   cellRef
	Text   string
	Gen    int
}

// editID is the ID of the input of the cell being edited.
var editID = gui.ScopeID("data-grid", "edit")

// editable reports whether the table's rows can be edited (SPEC §7.1).
func (tv *tableView) editable(a *App) bool {
	if tv.Table.IsView || tv.Loading || tv.Err != "" || len(tv.Columns) == 0 {
		return false
	}
	c, ok := a.conn(tv.Key.ConnID)
	return ok && !c.ReadOnly
}

func (tv *tableView) column(name string) (drivers.Column, bool) {
	i := slices.IndexFunc(tv.Columns, func(c drivers.Column) bool { return c.Name == name })
	if i < 0 {
		return drivers.Column{}, false
	}
	return tv.Columns[i], true
}

func (tv *tableView) hasPK() bool {
	return slices.ContainsFunc(tv.Columns, func(c drivers.Column) bool { return c.PK })
}

// columnEditable reports whether a column accepts edits: BLOBs are
// read-only.
func (tv *tableView) columnEditable(name string) bool {
	c, ok := tv.column(name)
	return ok && !query.IsBinaryType(strings.ToUpper(c.DBType))
}

// original returns the value read from the database for an existing row.
func (tv *tableView) original(row int, column string) (query.Value, bool) {
	res := tv.Data.Page.Result
	i := slices.IndexFunc(res.Columns, func(c query.Column) bool { return c.Name == column })
	if i < 0 || row < 0 || row >= len(res.Rows) {
		return query.Value{}, false
	}
	return res.Rows[row][i], true
}

func editValue(v query.Value) editing.Value {
	return editing.Value{Null: v.Null, Text: v.Text}
}

// cellValue is what a cell shows: its pending value or the original one.
// set is false for a column of a new row that was not set (DEFAULT).
func (tv *tableView) cellValue(ref cellRef) (v editing.Value, set bool) {
	p := &tv.Data.Pending
	if ref.New {
		return p.NewValue(ref.Row, ref.Column)
	}
	if v, ok := p.Value(ref.Row, ref.Column); ok {
		return v, true
	}
	orig, _ := tv.original(ref.Row, ref.Column)
	return editValue(orig), true
}

// withPKWarning runs action on an existing row; on a table without a PK it
// first warns that rows are identified by all their values (SPEC §7.3).
func withPKWarning(w *gui.Window, tv *tableView, existing bool, action func(w *gui.Window)) {
	if !existing || tv.hasPK() || tv.Data.NoPKOK {
		action(w)
		return
	}
	w.Dialog(gui.DialogCfg{
		DialogType: gui.DialogConfirm,
		Title:      "Table without a primary key",
		Body: tv.Table.Name + " has no primary key: rows are identified by the values of all their comparable columns. " +
			"If a change matches more than one row, nothing is saved. Edit anyway?",
		OnOkYes: func(w *gui.Window) {
			tv.Data.NoPKOK = true
			action(w)
		},
	})
}

// selectCell selects only ref's row with ref as the active cell, after
// committing the cell being edited. It returns false if that value was not
// valid.
func selectCell(w *gui.Window, tv *tableView, ref cellRef) bool {
	if !commitEdit(w, tv) {
		return false
	}
	selectRow(tv, ref.key(), ref.Column, 0)
	return true
}

// cellEditable reports whether a cell can take a value: editable column,
// row not deleted and not a BLOB.
func (tv *tableView) cellEditable(ref cellRef) bool {
	if !tv.columnEditable(ref.Column) {
		return false
	}
	if ref.New {
		return true
	}
	orig, ok := tv.original(ref.Row, ref.Column)
	return ok && !orig.Blob && !tv.Data.Pending.Deleted(ref.Row)
}

// startEdit selects a cell and opens its editor: the date editor for dates
// and times, an input for the rest.
func startEdit(w *gui.Window, tv *tableView, ref cellRef) {
	if !selectCell(w, tv, ref) || !tv.cellEditable(ref) {
		return
	}
	withPKWarning(w, tv, !ref.New, func(w *gui.Window) {
		col, _ := tv.column(ref.Column)
		if editing.TemporalKind(col.DBType) != editing.NotTemporal {
			openDateEditor(w, tv, ref, col)
			return
		}
		v, _ := tv.cellValue(ref)
		e := &tv.Data.Edit
		e.Active, e.Cell, e.Gen = true, ref, e.Gen+1
		e.Text = v.Text
		w.QueueCommand(func(w *gui.Window) { w.SetFocus(editID) })
	})
}

// endEdit closes the input without saving its text.
func endEdit(tv *tableView) {
	e := &tv.Data.Edit
	e.Active, e.Gen = false, e.Gen+1
}

// commitEdit records the text of the cell being edited. It returns false if
// the value is not valid (the input stays open).
func commitEdit(w *gui.Window, tv *tableView) bool {
	e := tv.Data.Edit
	if !e.Active {
		return true
	}
	return setCell(w, tv, e.Cell, editing.Text(e.Text))
}

// setCell validates v and records it as the cell's pending value, closing
// the input if it was editing that cell.
func setCell(w *gui.Window, tv *tableView, ref cellRef, v editing.Value) bool {
	col, ok := tv.column(ref.Column)
	if !ok {
		return false
	}
	if err := editing.Validate(col, v); err != nil {
		showWarning(w, err)
		return false
	}
	p := &tv.Data.Pending
	if ref.New {
		p.SetNew(ref.Row, ref.Column, v)
	} else {
		orig, _ := tv.original(ref.Row, ref.Column)
		p.Set(ref.Row, ref.Column, v, editValue(orig))
	}
	if e := tv.Data.Edit; e.Active && e.Cell == ref {
		endEdit(tv)
	}
	return true
}

// canSetNull reports whether Set NULL applies: a nullable column with some
// editable target cell.
func (tv *tableView) canSetNull() bool {
	refs := tv.targets()
	if len(refs) == 0 {
		return false
	}
	col, _ := tv.column(refs[0].Column)
	return col.Nullable
}

// canSetNow reports whether Set Now applies: a date/time column with some
// editable target cell.
func (tv *tableView) canSetNow() bool {
	refs := tv.targets()
	if len(refs) == 0 {
		return false
	}
	col, _ := tv.column(refs[0].Column)
	return editing.TemporalKind(col.DBType) != editing.NotTemporal
}

// setTargets sets v on every target cell (see targets).
func setTargets(w *gui.Window, tv *tableView, v editing.Value) {
	refs := tv.targets()
	existing := slices.ContainsFunc(refs, func(r cellRef) bool { return !r.New })
	withPKWarning(w, tv, existing, func(w *gui.Window) {
		for _, ref := range refs {
			if !setCell(w, tv, ref, v) {
				return
			}
		}
	})
}

// setNull sets the target cells to NULL (Ctrl+0 or Set NULL).
func setNull(w *gui.Window, tv *tableView) {
	if tv.canSetNull() {
		setTargets(w, tv, editing.Null)
	}
}

// setNow sets the target date/time cells to the current time (Set Now).
func setNow(w *gui.Window, tv *tableView) {
	if !tv.canSetNow() {
		return
	}
	col, _ := tv.column(tv.targets()[0].Column)
	setTargets(w, tv, editing.Text(editing.FormatTemporal(col.DBType, time.Now().Truncate(time.Second))))
}

// addRow adds a new row and edits its first column that is not
// autoincrement.
func addRow(w *gui.Window, tv *tableView) {
	if !commitEdit(w, tv) {
		return
	}
	id := tv.Data.Pending.AddRow()
	for _, c := range tv.Columns {
		if !c.AutoInc && tv.columnEditable(c.Name) {
			startEdit(w, tv, cellRef{Row: id, New: true, Column: c.Name})
			return
		}
	}
}

// toggleDelete marks an existing row as deleted (or restores it) and drops
// a new row.
func toggleDelete(w *gui.Window, tv *tableView, row int, isNew bool) {
	if e := tv.Data.Edit; e.Active && e.Cell.Row == row && e.Cell.New == isNew {
		endEdit(tv)
	}
	p := &tv.Data.Pending
	switch {
	case isNew:
		p.DeleteNew(row)
	case p.Deleted(row):
		p.Undelete(row)
	default:
		withPKWarning(w, tv, true, func(*gui.Window) { p.Delete(row) })
	}
}

func discardChanges(tv *tableView) {
	endEdit(tv)
	tv.Data.Sel.clear()
	tv.Data.Pending.Clear()
}

// reviewSave shows the SQL that will run and saves it on confirmation.
// then runs after a successful save.
func reviewSave(w *gui.Window, tv *tableView, then func(w *gui.Window)) {
	if !commitEdit(w, tv) || tv.Data.Pending.Len() == 0 {
		return
	}
	a := appOf(w)
	s, ok := a.Sessions.Get(tv.Key.ConnID)
	if !ok {
		return
	}
	lines, apply, err := saveWrites(s, tv)
	if err != nil {
		showError(w, err)
		return
	}
	note := "These statements run in one transaction. If one fails, nothing is saved."
	if s.IsMongo() && !s.Transactions {
		note = "This server does not support transactions (it is not a replica set): the changes are saved one by one, and if one fails the ones before it stay saved."
	}
	tv.Data.SaveErr = ""

	save := func(ctx gui.EventCtx) {
		ctx.Consume()
		if tv.Data.Saving {
			return
		}
		tv.Data.Saving, tv.Data.SaveErr = true, ""
		async(ctx.Window, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, apply(ctx)
		}, func(w *gui.Window, _ struct{}, err error) {
			tv.Data.Saving = false
			if err != nil {
				tv.Data.SaveErr = errText(err)
				if s.IsMongo() && !s.Transactions {
					loadData(w, tv, query.Reload) // some changes may be saved: show them
				}
				return
			}
			w.DialogDismiss()
			discardChanges(tv)
			appOf(w).Status = fmt.Sprintf("Saved %d changes to %s", len(lines), tv.Table.Name)
			if then != nil {
				// then leaves this page; it is reloaded when shown again.
				tv.Data.fetched = false
				then(w)
				return
			}
			loadData(w, tv, query.Reload)
		})
	}

	w.Dialog(gui.DialogCfg{
		ID:         "save-changes",
		DialogType: gui.DialogCustom,
		Title:      fmt.Sprintf("Save %d changes?", len(lines)),
		Width:      640,
		FocusID:    "save-confirm",
		CustomView: func(w *gui.Window) gui.View {
			t := gui.CurrentTheme()
			content := []gui.View{
				gui.Text(gui.TextCfg{
					Text:      note,
					TextStyle: t.TextStyleSecondary,
					Mode:      gui.TextModeWrap,
				}),
				sqlPreview("save-sql", strings.Join(lines, "\n")),
			}
			if tv.Data.SaveErr != "" {
				content = append(content, components.ErrorText(tv.Data.SaveErr))
			}
			content = append(content, components.DialogButtons(
				components.DismissButton("save-cancel", "Cancel", gui.ButtonSecondary),
				gui.Button(gui.ButtonCfg{
					ID:       "save-confirm",
					Label:    "Save",
					Variant:  gui.ButtonPrimary,
					Disabled: tv.Data.Saving,
					OnClick:  save,
				}),
			))
			return components.DialogBody(content...)
		},
	})
}

// saveWrites builds the pending changes of tv: the lines of the review
// and the function that saves them (SQL DML or MongoDB writes by _id).
func saveWrites(s *session.Session, tv *tableView) ([]string, func(context.Context) error, error) {
	changes := tv.Data.Pending.Changes()
	if s.IsMongo() {
		writes, err := mongodb.Build(tv.Columns, tv.Data.Page.Result, changes)
		if err != nil {
			return nil, nil, err
		}
		lines := make([]string, len(writes))
		for i, wr := range writes {
			lines[i] = wr.Preview(tv.Table.Name)
		}
		coll := s.Collection(tv.Key.Database, tv.Table)
		return lines, func(ctx context.Context) error { return mongodb.Apply(ctx, coll, writes, s.Transactions) }, nil
	}

	target := editing.Target{Driver: s.Driver, Table: tv.Table, Columns: tv.Columns, Page: tv.Data.Page.Result}
	stmts, err := editing.Build(target, changes)
	if err != nil {
		return nil, nil, err
	}
	lines := make([]string, len(stmts))
	for i, st := range stmts {
		lines[i] = st.Preview(s.Driver) + ";"
	}
	return lines, func(ctx context.Context) error {
		db, err := s.Pool(ctx, tv.Key.Database)
		if err != nil {
			return err
		}
		return editing.Apply(ctx, db, stmts)
	}, nil
}

// pendingView returns the first view of the chain (the current one and the
// ones it was opened from) that has pending changes.
func pendingView(tv *tableView) *tableView {
	for ; tv != nil; tv = tv.Back {
		if tv.Data.Pending.Len() > 0 || tv.Data.Edit.Active {
			return tv
		}
	}
	return nil
}

// leaveTable runs then if tv has no pending changes, or after the user
// saves or discards them (SPEC §7.2: changing page, closing the table or
// disconnecting). chain also checks the views tv was opened from.
func leaveTable(w *gui.Window, tv *tableView, chain bool, then func(w *gui.Window)) {
	if tv == nil {
		then(w)
		return
	}
	dirty := tv
	if chain {
		dirty = pendingView(tv)
	} else if tv.Data.Pending.Len() == 0 && !tv.Data.Edit.Active {
		dirty = nil
	}
	if dirty == nil {
		then(w)
		return
	}
	if !commitEdit(w, dirty) {
		return
	}
	if dirty.Data.Pending.Len() == 0 {
		leaveTable(w, tv, chain, then)
		return
	}

	w.Dialog(gui.DialogCfg{
		ID:         "unsaved-changes",
		DialogType: gui.DialogCustom,
		Title:      "Unsaved changes",
		Width:      440,
		FocusID:    "unsaved-cancel",
		CustomView: func(w *gui.Window) gui.View {
			t := gui.CurrentTheme()
			return components.DialogBody(
				gui.Text(gui.TextCfg{
					Text:      fmt.Sprintf("%s has %d unsaved changes.", dirty.Table.Name, dirty.Data.Pending.Len()),
					TextStyle: t.TextStyleSecondary,
					Mode:      gui.TextModeWrap,
				}),
				components.DialogButtons(
					components.DismissButton("unsaved-cancel", "Cancel", gui.ButtonSecondary),
					gui.Button(gui.ButtonCfg{
						ID:      "unsaved-discard",
						Label:   "Discard",
						Variant: gui.ButtonDanger,
						OnClick: func(ctx gui.EventCtx) {
							ctx.Consume()
							ctx.Window.DialogDismiss()
							discardChanges(dirty)
							leaveTable(ctx.Window, tv, chain, then)
						},
					}),
					gui.Button(gui.ButtonCfg{
						ID:      "unsaved-save",
						Label:   "Save…",
						Variant: gui.ButtonPrimary,
						OnClick: func(ctx gui.EventCtx) {
							ctx.Consume()
							// The review dialog replaces this one.
							reviewSave(ctx.Window, dirty, func(w *gui.Window) { leaveTable(w, tv, chain, then) })
						},
					}),
				),
			)
		},
	})
}

// unsavedChanges counts the pending changes of every tab, with the views
// each one was opened from, including a cell being edited.
func unsavedChanges(a *App) (n int, tables []string) {
	for _, t := range a.Tabs {
		for tv := t.Table; tv != nil; tv = tv.Back {
			c := tv.Data.Pending.Len()
			if tv.Data.Edit.Active {
				c++
			}
			if c > 0 {
				n += c
				tables = append(tables, tv.Table.Name)
			}
		}
	}
	return n, tables
}

// confirmClose is the window's OnCloseRequest: with unsaved changes it asks
// to discard them or keep editing; otherwise it closes.
func confirmClose(w *gui.Window) {
	n, tables := unsavedChanges(appOf(w))
	if n == 0 {
		w.Close()
		return
	}
	w.Dialog(gui.DialogCfg{
		ID:         "close-unsaved",
		DialogType: gui.DialogCustom,
		Title:      "Unsaved changes",
		Width:      440,
		FocusID:    "close-keep",
		CustomView: func(w *gui.Window) gui.View {
			t := gui.CurrentTheme()
			return components.DialogBody(
				gui.Text(gui.TextCfg{
					Text: fmt.Sprintf("%s has %d unsaved changes. If you close Anvil they are lost.",
						strings.Join(tables, ", "), n),
					TextStyle: t.TextStyleSecondary,
					Mode:      gui.TextModeWrap,
				}),
				components.DialogButtons(
					components.DismissButton("close-keep", "Keep editing", gui.ButtonPrimary),
					gui.Button(gui.ButtonCfg{
						ID:      "close-discard",
						Label:   "Discard and close",
						Variant: gui.ButtonDanger,
						OnClick: func(ctx gui.EventCtx) {
							ctx.Window.DialogDismiss()
							ctx.Window.Close()
							ctx.Consume()
						},
					}),
				),
			)
		},
	})
}
