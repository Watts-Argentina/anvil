package ui

import (
	"strconv"

	"github.com/Watts-Argentina/anvil/internal/mongodb"
	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/ui/components"
	"github.com/go-gui-org/go-gui/gui"
)

// The Documents view of a MongoDB collection (SPEC §2.3): one card per
// document with a field per line. Objects and arrays expand to show their
// fields, read-only; top-level fields are edited like grid cells, with the
// same pending changes, selection and edit bar.

// docKey is a field of a document in the Documents view: Path is the
// top-level field and, below it, the keys of the nested fields.
type docKey struct {
	Row  int
	New  bool
	Path string
}

// keyWidth is the width of the field names column.
const keyWidth = 180

func docList(a *App, tv *tableView) gui.View {
	t := gui.CurrentTheme()
	d := &tv.Data
	edit := tv.editable(a)
	st := newDataStyles(t)

	var cards []gui.View
	if edit {
		for _, id := range d.Pending.NewRows() {
			cards = append(cards, docCard(tv, st, rowKey{Row: id, New: true}, edit))
		}
	}
	for r := range d.Page.Rows {
		cards = append(cards, docCard(tv, st, rowKey{Row: r}, edit))
	}
	if len(cards) == 0 {
		cards = append(cards, gui.Text(gui.TextCfg{Text: "No documents.", TextStyle: t.TextStyleSecondary}))
	}
	return gui.Column(gui.ContainerCfg{
		ID:         "docs",
		Sizing:     gui.FillFill,
		Scrollable: true,
		ScrollMode: gui.ScrollVerticalOnly,
		Padding:    gui.NewPadding(0, 8, 0, 0), // room for the scrollbar
		Spacing:    gui.SpacingSmall,
		SizeBorder: gui.NoBorder,
		Content:    cards,
	})
}

// docCard is a document: its number (click to select it, Ctrl/Shift to add
// or extend) and its fields. A new document lists every known field, unset.
func docCard(tv *tableView, st *dataStyles, k rowKey, edit bool) gui.View {
	t := gui.CurrentTheme()
	d := &tv.Data
	res := d.Page.Result

	label, style := "#"+itoa(k.Row+1), st.number
	switch {
	case k.New:
		label, style = "New document", st.added
	case edit && d.Pending.Deleted(k.Row):
		label, style = label+" (deleted)", st.removed
	}
	header := gui.Row(gui.ContainerCfg{
		Sizing:     gui.FillFit,
		Padding:    gui.PadVH(2, 4),
		SizeBorder: gui.NoBorder,
		Radius:     gui.RadiusSmall,
		Content:    []gui.View{gui.Text(gui.TextCfg{Text: label, TextStyle: style})},
	})
	if edit {
		header = gui.Row(gui.ContainerCfg{
			Sizing:     gui.FillFit,
			Padding:    gui.PadVH(2, 4),
			SizeBorder: gui.NoBorder,
			Radius:     gui.RadiusSmall,
			Hover:      gui.HoverStyle{Color: t.ColorHover, Cursor: gui.CursorPointingHand},
			OnClick:    clickRowNumber(tv, k),
			Content:    []gui.View{gui.Text(gui.TextCfg{Text: label, TextStyle: style})},
		})
	}

	lines := []gui.View{header}
	for c, col := range res.Columns {
		ref := cellRef{Row: k.Row, New: k.New, Column: col.Name}
		var orig query.Value
		var cell gui.TableCellCfg
		typ := col.DBType
		if k.New {
			if !tv.columnEditable(col.Name) {
				continue
			}
			cell = newCell(tv, st, ref, "not set")
		} else {
			orig = res.Rows[k.Row][c]
			if _, pending := d.Pending.Value(k.Row, col.Name); mongodb.Missing(orig) && !pending {
				continue // the document does not have it
			}
			cell, _ = pageCell(tv, st, ref, orig, edit)
			if tn := mongodb.TypeOf(orig); tn != "" {
				typ = tn
			}
		}
		lines = append(lines, topField(tv, st, ref, cell, orig, typ, edit)...)
	}

	border := t.ColorBorder
	if edit && d.Sel.has(k) {
		border = t.ColorAccent
	}
	return gui.Column(gui.ContainerCfg{
		Sizing:      gui.FillFit,
		Padding:     gui.PaddingSmall,
		Spacing:     gui.SpacingTight,
		SizeBorder:  gui.BorderThin,
		ColorBorder: border,
		Radius:      gui.RadiusMedium,
		Content:     lines,
	})
}

// topField is a top-level field: editable on click, and expandable if it
// is an object or an array (while it has no pending value).
func topField(tv *tableView, st *dataStyles, ref cellRef, cell gui.TableCellCfg, orig query.Value, typ string, edit bool) []gui.View {
	d := &tv.Data
	key := docKey{Row: ref.Row, New: ref.New, Path: ref.Column}
	var children []mongodb.Field
	if _, pending := d.Pending.Value(ref.Row, ref.Column); !ref.New && !pending {
		children = mongodb.Fields(orig)
	}

	var value gui.View
	switch {
	case edit && d.Edit.Active && d.Edit.Cell == ref:
		value = cellInput(tv, ref)
	case edit:
		value = gui.Row(gui.ContainerCfg{
			Sizing:     gui.FillFit,
			Padding:    gui.NoPadding,
			SizeBorder: gui.NoBorder,
			Hover:      gui.HoverStyle{Cursor: gui.CursorIBeam},
			OnClick:    clickCell(tv, ref),
			Content:    []gui.View{cellLabel(cell, children, d.Open[key])},
		})
	default:
		value = cellLabel(cell, children, d.Open[key])
	}

	out := []gui.View{fieldLine(tv, key, 0, children != nil, ref.Column, value, typ)}
	if d.Open[key] {
		out = append(out, nestedFields(tv, st, key, children, 1)...)
	}
	return out
}

// nestedFields are the read-only fields of an expanded object or array.
func nestedFields(tv *tableView, st *dataStyles, parent docKey, fields []mongodb.Field, level int) []gui.View {
	var out []gui.View
	for _, f := range fields {
		key := docKey{Row: parent.Row, New: parent.New, Path: parent.Path + "\x00" + f.Key}
		children := mongodb.Fields(f.Value)
		value := cellLabel(st.valueCell(f.Value), children, tv.Data.Open[key])
		out = append(out, fieldLine(tv, key, level, children != nil, f.Key, value, f.Type))
		if tv.Data.Open[key] {
			out = append(out, nestedFields(tv, st, key, children, level+1)...)
		}
	}
	return out
}

// cellLabel is the text of a value, clipped to the line. An open object or
// array shows its size instead of its JSON: its fields follow.
func cellLabel(cell gui.TableCellCfg, children []mongodb.Field, open bool) gui.View {
	text := cell.Value
	if open {
		text = sizeText(children)
	}
	style := gui.CurrentTheme().TextStyleDef
	if cell.TextStyle != nil {
		style = *cell.TextStyle
	}
	return components.TreeLabel(text, style)
}

func sizeText(children []mongodb.Field) string {
	n := strconv.Itoa(len(children))
	if len(children) > 0 && children[0].Key[0] == '[' {
		return "[" + n + " items]"
	}
	return "{" + n + " fields}"
}

// fieldLine is one line of a card: the expand chevron (if the value has
// fields), the name, the value and its type.
func fieldLine(tv *tableView, key docKey, level int, expandable bool, name string, value gui.View, typ string) gui.View {
	t := gui.CurrentTheme()
	chevron := gui.View(gui.Row(gui.ContainerCfg{Width: 16, Sizing: gui.FixedFit, Padding: gui.NoPadding, SizeBorder: gui.NoBorder}))
	if expandable {
		chevron = gui.Row(gui.ContainerCfg{
			Width:      16,
			Sizing:     gui.FixedFit,
			Padding:    gui.NoPadding,
			SizeBorder: gui.NoBorder,
			Hover:      gui.HoverStyle{Cursor: gui.CursorPointingHand},
			A11YCfg:    gui.A11YCfg{A11YLabel: "Expand " + name},
			OnClick: func(ctx gui.EventCtx) {
				toggleField(tv, key)
				ctx.Consume()
			},
			Content: []gui.View{components.Chevron(tv.Data.Open[key])},
		})
	}
	return gui.Row(gui.ContainerCfg{
		Sizing:     gui.FillFit,
		Padding:    gui.NewPadding(1, 4, 1, float32(level*components.TreeIndent)),
		Spacing:    gui.SpacingSmall,
		SizeBorder: gui.NoBorder,
		VAlign:     gui.VAlignMiddle,
		Content: []gui.View{
			chevron,
			gui.Row(gui.ContainerCfg{
				Width:      max(keyWidth-float32(level*components.TreeIndent), 60),
				Sizing:     gui.FixedFit,
				Padding:    gui.NoPadding,
				SizeBorder: gui.NoBorder,
				Clip:       true,
				Content:    []gui.View{gui.Text(gui.TextCfg{Text: name, TextStyle: t.TextStyleSecondary})},
			}),
			value,
			gui.Text(gui.TextCfg{Text: typ, TextStyle: t.TextStyleCaption}),
		},
	})
}

// toggleField expands or collapses an object or array.
func toggleField(tv *tableView, key docKey) {
	if tv.Data.Open == nil {
		tv.Data.Open = map[docKey]bool{}
	}
	tv.Data.Open[key] = !tv.Data.Open[key]
}
