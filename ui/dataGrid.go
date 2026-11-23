package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/Watts-Argentina/anvil/internal/editing"
	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/ui/components"
	"github.com/go-gui-org/go-gui/gui"
)

// editMinChars is the minimum width, in characters, of a column while one
// of its cells is being edited.
const editMinChars = 16

// dataStyles adds the styles of pending changes to the cell styles
// (SPEC §7.2): modified cells highlighted, new rows in the success color and
// deleted rows struck through.
type dataStyles struct {
	cellStyles
	placeholder gui.TextStyle // DEFAULT in new rows
	added       gui.TextStyle
	deleted     gui.TextStyle
	removed     gui.TextStyle // the number of deleted rows
	number      gui.TextStyle // the # column
	accent      gui.Color
	right       gui.HorizontalAlign
}

func newDataStyles(t gui.Theme) *dataStyles {
	st := &dataStyles{
		cellStyles:  newCellStyles(t),
		placeholder: t.TextStylePlaceholder,
		added:       t.TextStyleDef,
		deleted:     t.TextStyleDisabled,
		removed:     t.TextStyleError,
		number:      t.TextStyleSecondary,
		accent:      t.ColorAccent,
		right:       gui.HAlignEnd,
	}
	st.added.Color = t.TextStyleSuccess.Color
	st.removed.Strikethrough = true
	st.deleted.Strikethrough = true
	return st
}

// modified marks a changed cell with a highlighted background.
func modified(style gui.TextStyle) *gui.TextStyle {
	style.BgColor = gui.CurrentTheme().ColorWarningSubtle
	return &style
}

// active underlines the active cell of the selection.
func active(cell gui.TableCellCfg) gui.TableCellCfg {
	style := gui.CurrentTheme().TextStyleDef
	if cell.TextStyle != nil {
		style = *cell.TextStyle
	}
	style.Underline = true
	cell.TextStyle = &style
	return cell
}

// inSelection tints the text background of a selected row's cell, unless
// it already has the highlight of a modified value.
func inSelection(cell gui.TableCellCfg) gui.TableCellCfg {
	style := gui.CurrentTheme().TextStyleDef
	if cell.TextStyle != nil {
		if cell.TextStyle.BgColor != (gui.Color{}) {
			return cell
		}
		style = *cell.TextStyle
	}
	style.BgColor = gui.CurrentTheme().ColorAccentSubtle
	cell.TextStyle = &style
	return cell
}

// numberCell is the # column: the row's number in the grid (+ for new
// rows), filled with the accent color when the row is selected. Clicking it
// selects the row (Ctrl/Shift to add or extend).
func numberCell(tv *tableView, st *dataStyles, k rowKey, label string, style *gui.TextStyle, edit bool) gui.TableCellCfg {
	if style == nil {
		style = &st.number
	}
	cell := gui.TableCellCfg{Value: label, TextStyle: style, HAlign: &st.right}
	if !edit {
		return cell
	}
	if tv.Data.Sel.has(k) {
		t := gui.CurrentTheme()
		text := *style
		text.Color = t.ColorTextOnAccent
		text.Strikethrough = false
		cell.Content = gui.Row(gui.ContainerCfg{
			Sizing:     gui.FillFit,
			Padding:    gui.PadVH(0, 4),
			SizeBorder: gui.NoBorder,
			Radius:     gui.RadiusSmall,
			Color:      t.ColorAccent,
			HAlign:     gui.HAlignEnd,
			Content:    []gui.View{gui.Text(gui.TextCfg{Text: label, TextStyle: text})},
		})
	}
	cell.OnClick = clickRowNumber(tv, k)
	return cell
}

// dataGrid is the Data tab grid. Its first column (#) numbers the rows from
// 1 and shows the selection; new rows go on top, before the page rows.
// Clicking a cell selects its row and opens its editor; Ctrl/Shift+click
// select several rows.
func dataGrid(a *App, tv *tableView) gui.View {
	t := gui.CurrentTheme()
	d := &tv.Data
	res := d.Page.Result
	if len(res.Columns) == 0 {
		return gui.Text(gui.TextCfg{Text: "No columns.", TextStyle: t.TextStyleSecondary})
	}
	edit := tv.editable(a)
	st := newDataStyles(t)

	head := headRow(res)
	// The width of the # column fits the largest number.
	width := strings.Repeat("0", len(itoa(len(res.Rows)+len(d.Pending.NewRows()))))
	head.Cells = append([]gui.TableCellCfg{{Value: "#" + width, Content: gui.Text(gui.TextCfg{Text: "#", TextStyle: t.TextStyleSecondary}), HeadCell: true}}, head.Cells...)
	rows := make([]gui.TableRowCfg, 0, len(res.Rows)+1)
	rows = append(rows, head)
	if edit {
		for _, id := range d.Pending.NewRows() {
			rows = append(rows, newRow(tv, st, id))
		}
	}
	for r := range res.Rows {
		rows = append(rows, pageRow(tv, st, r, edit))
	}
	return components.GridTable("data-grid", rows, &d.gridSize)
}

// pageRow is a row read from the database, with its pending changes.
func pageRow(tv *tableView, st *dataStyles, r int, edit bool) gui.TableRowCfg {
	d := &tv.Data
	res := d.Page.Result
	k := rowKey{Row: r}
	deleted := edit && d.Pending.Deleted(r)
	cells := make([]gui.TableCellCfg, 0, len(res.Columns)+1)
	var numStyle *gui.TextStyle
	if deleted {
		numStyle = &st.removed
	}
	cells = append(cells, numberCell(tv, st, k, itoa(r+1), numStyle, edit))

	for c, col := range res.Columns {
		ref := cellRef{Row: r, Column: col.Name}
		if edit && d.Edit.Active && d.Edit.Cell == ref {
			cells = append(cells, editCell(tv, ref))
			continue
		}
		orig := res.Rows[r][c]
		cell, changed := pageCell(tv, st, ref, orig, edit)
		// The link follows the original value, so it is hidden while the
		// cell has another one.
		if fk, ok := tv.fkOf(col.Name); ok && !orig.Null && !changed {
			row := res.Rows[r]
			cell = components.WithButton(cell, components.CellButton(gui.ScopeIDN("data-grid", "link", r*len(res.Columns)+c),
				"link", "Open in "+fk.RefTable.Name, st.accent, func(w *gui.Window) { followFK(w, tv, fk, row) }))
		}
		if edit {
			cell.OnClick = clickCell(tv, ref)
		}
		cells = append(cells, cell)
	}
	return gui.TableRowCfg{Cells: cells}
}

// newRow is a row added in the grid, not yet inserted.
func newRow(tv *tableView, st *dataStyles, id int) gui.TableRowCfg {
	d := &tv.Data
	res := d.Page.Result
	k := rowKey{Row: id, New: true}
	cells := make([]gui.TableCellCfg, 0, len(res.Columns)+1)
	cells = append(cells, numberCell(tv, st, k, "+", &st.added, true))

	for _, col := range res.Columns {
		ref := cellRef{Row: id, New: true, Column: col.Name}
		if d.Edit.Active && d.Edit.Cell == ref {
			cells = append(cells, editCell(tv, ref))
			continue
		}
		cell := newCell(tv, st, ref, "DEFAULT")
		cell.OnClick = clickCell(tv, ref)
		cells = append(cells, cell)
	}
	return gui.TableRowCfg{Cells: cells}
}

// pageCell is a cell of a page row with its pending value, struck through
// if the row is deleted and tinted if it is selected. changed reports that
// it shows a pending value.
func pageCell(tv *tableView, st *dataStyles, ref cellRef, orig query.Value, edit bool) (gui.TableCellCfg, bool) {
	d := &tv.Data
	cell := st.valueCell(orig)
	changed := false
	if pv, ok := d.Pending.Value(ref.Row, ref.Column); edit && ok {
		cell, changed = st.editedCell(pv), true
	}
	if edit && d.Pending.Deleted(ref.Row) {
		cell.TextStyle = &st.deleted
	}
	return selected(tv, ref, cell, edit), changed
}

// newCell is a cell of a new row: its value, or unset (DEFAULT).
func newCell(tv *tableView, st *dataStyles, ref cellRef, unset string) gui.TableCellCfg {
	var cell gui.TableCellCfg
	if v, ok := tv.Data.Pending.NewValue(ref.Row, ref.Column); ok {
		cell = st.editedCell(v)
		if !v.Null {
			cell.TextStyle = &st.added
		}
	} else {
		cell = gui.TableCellCfg{Value: unset, TextStyle: &st.placeholder}
	}
	return selected(tv, ref, cell, true)
}

// selected tints a cell of a selected row and marks the active one.
func selected(tv *tableView, ref cellRef, cell gui.TableCellCfg, edit bool) gui.TableCellCfg {
	s := tv.Data.Sel
	if !edit || !s.has(ref.key()) {
		return cell
	}
	cell = inSelection(cell)
	if s.Column == ref.Column {
		cell = active(cell)
	}
	return cell
}

// editedCell shows a pending value, highlighted.
func (st *dataStyles) editedCell(v editing.Value) gui.TableCellCfg {
	if v.Null {
		return gui.TableCellCfg{Value: "NULL", TextStyle: modified(st.null)}
	}
	return gui.TableCellCfg{Value: components.CellText(v.Text), TextStyle: modified(gui.CurrentTheme().TextStyleDef)}
}

// editCell is the input of the cell being edited. Enter or leaving the cell
// keeps the value, Escape discards it and Ctrl+0 sets NULL.
func editCell(tv *tableView, ref cellRef) gui.TableCellCfg {
	e := tv.Data.Edit

	width := components.CellText(e.Text) + components.ButtonRoom
	if n := utf8.RuneCountInString(width); n < editMinChars {
		width += strings.Repeat(" ", 2*(editMinChars-n))
	}
	return gui.TableCellCfg{Value: width, Content: cellInput(tv, ref)}
}

// cellInput is the input of the cell being edited, in the grid or in the
// Documents view. Enter or leaving it keeps the value, Escape discards it
// and Ctrl+0 sets NULL.
func cellInput(tv *tableView, ref cellRef) gui.View {
	e := tv.Data.Edit
	gen := e.Gen
	current := func() bool { cur := tv.Data.Edit; return cur.Active && cur.Gen == gen }
	return gui.Input(gui.InputCfg{
		ID:          editID,
		Text:        e.Text,
		Sizing:      gui.FillFit,
		Padding:     gui.PadVH(0, 2),
		Placeholder: "''",
		A11YCfg:     gui.A11YCfg{A11YLabel: "Value of " + ref.Column},
		OnTextChanged: func(s string, ctx gui.EventCtx) {
			if current() {
				tv.Data.Edit.Text = s
			}
		},
		OnTextCommit: func(s string, _ gui.InputCommitReason, ctx gui.EventCtx) {
			if current() {
				tv.Data.Edit.Text = s
				setCell(ctx.Window, tv, ref, editing.Text(s))
			}
		},
		OnKeyDown: func(ctx gui.EventCtx) {
			ev := ctx.Event
			if ev == nil || !current() {
				return
			}
			switch {
			case ev.KeyCode == gui.KeyEscape:
				endEdit(tv)
				ctx.Consume()
			case ev.KeyCode == gui.Key0 && ev.Modifiers.Has(gui.ModCtrl):
				setNull(ctx.Window, tv)
				ctx.Consume()
			}
		},
	})
}

// pendingSummary describes the pending changes for the toolbar.
func pendingSummary(p *editing.Pending) string {
	var ins, upd, del int
	for _, c := range p.Changes() {
		switch c.Kind {
		case editing.Insert:
			ins++
		case editing.Update:
			upd++
		case editing.Delete:
			del++
		}
	}
	var parts []string
	for _, x := range []struct {
		n    int
		what string
	}{{ins, "new"}, {upd, "edited"}, {del, "deleted"}} {
		if x.n > 0 {
			parts = append(parts, itoa(x.n)+" "+x.what)
		}
	}
	return strings.Join(parts, ", ")
}
