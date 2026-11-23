package ui

import (
	"github.com/Watts-Argentina/anvil/internal/completion"
	"github.com/Watts-Argentina/anvil/ui/components"
	"github.com/go-gui-org/go-edit/edit"
	"github.com/go-gui-org/go-edit/edit/buffer"
	"github.com/go-gui-org/go-gui/gui"
)

// Editor actions of the autocompletion popup (SPEC §5.1).
const (
	actionCompleteSync   = "anvil.complete.sync"   // after an edit: recompute the popup
	actionCompleteShow   = "anvil.complete.show"   // Ctrl+Space
	actionCompletePrev   = "anvil.complete.prev"   // Up
	actionCompleteNext   = "anvil.complete.next"   // Down
	actionCompleteAccept = "anvil.complete.accept" // Enter, Tab, click
	actionCompleteClose  = "anvil.complete.close"  // Escape
)

// completionRows is how many candidates the popup shows at once.
const completionRows = 8

// completionState is the autocompletion popup of an SQL editor.
type completionState struct {
	Open  bool
	Items []completion.Candidate
	Sel   int
	// Cursor position the items were computed for; the popup closes when
	// the cursor moves elsewhere.
	Line, Col int
	X, Y      float32 // caret, relative to the editor's text area
	// Offset of the text area inside the editor (its padding), measured
	// from the layout.
	TextX, TextY float32

	edited  bool // the buffer changed since the last frame
	typed   bool // the last edit typed an identifier character or "."
	syncing bool // a sync action is queued
}

// watchEdits records the editor's edits for the popup.
func (ed *sqlEditor) watchEdits() {
	ed.Buf.OnEdit(func(c buffer.Change) {
		b := c.Applied.NewBytes
		ed.comp.edited = true
		ed.comp.typed = len(b) == 1 && (b[0] == '.' || b[0] == '_' ||
			b[0] >= 'a' && b[0] <= 'z' || b[0] >= 'A' && b[0] <= 'Z' || b[0] >= '0' && b[0] <= '9')
	})
}

// syncCompletion runs every frame: after an edit it asks the editor for the
// cursor (only actions see it), and it closes the popup when the cursor
// moved without editing (arrows, clicks).
func syncCompletion(w *gui.Window, ed *sqlEditor) {
	c := &ed.comp
	if c.edited {
		c.edited = false
		if c.typed || c.Open {
			c.syncing = true
			// Out of the view: TriggerAction writes the editor state.
			w.QueueCommand(func(w *gui.Window) { edit.TriggerAction(w, ed.ID, actionCompleteSync) })
		}
		return
	}
	if c.Open && !c.syncing {
		if line, col, ok := edit.CursorPos(w, ed.ID); !ok || line != c.Line || col != c.Col {
			c.Open = false
		}
	}
}

// The actions are generic for the same reason as runAction: go-edit passes
// its unexported state type.

func syncAction[S any](cfg edit.EditorCfg, st *S, buf *buffer.Buffer, w *gui.Window) {
	complete(false, cfg, st, buf, w)
}

func showAction[S any](cfg edit.EditorCfg, st *S, buf *buffer.Buffer, w *gui.Window) {
	complete(true, cfg, st, buf, w)
}

// complete computes the candidates for the word before the cursor. force
// (Ctrl+Space) opens the popup even if the last edit was not typing.
func complete[S any](force bool, cfg edit.EditorCfg, st *S, buf *buffer.Buffer, w *gui.Window) {
	{
		f := fieldsOf(st)
		cur := f.cursor()
		if cur == nil {
			return
		}
		p := cur.Cursor
		word := completion.WordAt(string(buf.Line(p.Line)), p.ByteCol)
		x, y, _ := f.caret(buf, cfg.ShowLineNumbers)
		text := buf.String()
		hasSel := cur.HasSelection()
		// Actions run inside the layout pass: change the app state after it.
		w.QueueCommand(func(w *gui.Window) {
			a := appOf(w)
			ed := editorByID(a, cfg.ID)
			if ed == nil {
				return
			}
			c := &ed.comp
			show := force || c.typed || c.Open
			c.syncing, c.typed = false, false
			if !show || hasSel {
				c.Open = false
				return
			}
			conn, _ := a.conn(ed.Key.ConnID)
			tables := a.Tables[ed.Key]
			loadColumns(w, ed.Key, completion.Mentioned(text, tables))
			c.Items = completion.Complete(word, completion.Source{
				Keywords: completion.Keywords(conn.Engine),
				Tables:   tables,
				Columns:  a.Columns[ed.Key],
			})
			c.Open, c.Sel = len(c.Items) > 0, 0
			c.Line, c.Col, c.X, c.Y = p.Line, p.ByteCol, x, y
		})
	}
}

func prevAction[S any](cfg edit.EditorCfg, _ *S, _ *buffer.Buffer, w *gui.Window) {
	moveSelection(w, cfg.ID, -1)
}

func nextAction[S any](cfg edit.EditorCfg, _ *S, _ *buffer.Buffer, w *gui.Window) {
	moveSelection(w, cfg.ID, 1)
}

// moveSelection moves the popup selection by d, wrapping around.
func moveSelection(w *gui.Window, id string, d int) {
	if ed := editorByID(appOf(w), id); ed != nil && len(ed.comp.Items) > 0 {
		n := len(ed.comp.Items)
		ed.comp.Sel = (ed.comp.Sel + d + n) % n
	}
}

func closeAction[S any](cfg edit.EditorCfg, _ *S, _ *buffer.Buffer, w *gui.Window) {
	if ed := editorByID(appOf(w), cfg.ID); ed != nil {
		ed.comp.Open = false
	}
}

// acceptAction replaces the word before the cursor with the selected
// candidate.
func acceptAction[S any](cfg edit.EditorCfg, st *S, buf *buffer.Buffer, w *gui.Window) {
	ed := editorByID(appOf(w), cfg.ID)
	cur := fieldsOf(st).cursor()
	if ed == nil || cur == nil || !ed.comp.Open {
		return
	}
	c := &ed.comp
	c.Open = false
	p := cur.Cursor
	if p.Line != c.Line || c.Sel >= len(c.Items) {
		return
	}
	// shortcut: identifiers are inserted unquoted; quote them (QuoteIdent) if
	// names with spaces or mixed case in PG turn out to be common.
	item := c.Items[c.Sel].Text
	if c.Items[c.Sel].Kind == completion.Table {
		// Its columns are likely next ("table.").
		key := ed.Key
		w.QueueCommand(func(w *gui.Window) {
			loadColumns(w, key, completion.Mentioned(item, appOf(w).Tables[key]))
		})
	}
	start := buffer.Position{Line: p.Line, ByteCol: completion.WordAt(string(buf.Line(p.Line)), p.ByteCol).Start}
	buf.SetUndoCursor(p, cur.Anchor)
	buf.Apply(buffer.Edit{Range: buffer.Range{Start: start, End: p}, NewBytes: []byte(item)})
	cur.Cursor = buffer.Position{Line: p.Line, ByteCol: start.ByteCol + len(item)}
	c.edited, c.typed = false, false // our own edit does not reopen the popup
}

// completionKeymap binds Ctrl+Space and, while the popup is open, the keys
// that drive it (on top of the editor's own bindings).
func completionKeymap(open bool) *edit.Keymap {
	km := &edit.Keymap{Name: "anvil.complete", Bindings: []edit.Binding{
		{Key: gui.KeySpace, Modifiers: gui.ModCtrl, ActionID: actionCompleteShow},
	}}
	if open {
		km.Bindings = append(km.Bindings,
			edit.Binding{Key: gui.KeyUp, ActionID: actionCompletePrev},
			edit.Binding{Key: gui.KeyDown, ActionID: actionCompleteNext},
			edit.Binding{Key: gui.KeyEnter, ActionID: actionCompleteAccept},
			edit.Binding{Key: gui.KeyTab, ActionID: actionCompleteAccept},
			edit.Binding{Key: gui.KeyEscape, ActionID: actionCompleteClose},
		)
	}
	return km
}

func completionActions() map[string]edit.Action {
	return map[string]edit.Action{
		actionCompleteSync:   {ID: actionCompleteSync, PreservesAnchor: true, Execute: syncAction},
		actionCompleteShow:   {ID: actionCompleteShow, PreservesAnchor: true, Execute: showAction},
		actionCompletePrev:   {ID: actionCompletePrev, PreservesAnchor: true, Execute: prevAction},
		actionCompleteNext:   {ID: actionCompleteNext, PreservesAnchor: true, Execute: nextAction},
		actionCompleteClose:  {ID: actionCompleteClose, PreservesAnchor: true, Execute: closeAction},
		actionCompleteAccept: {ID: actionCompleteAccept, Execute: acceptAction},
	}
}

// measureText is the AmendLayout of the editor's wrapper: it records where
// go-edit's text canvas (the editor's first child) starts.
func (ed *sqlEditor) measureText(ctx gui.EventCtx) {
	l := ctx.Layout
	if len(l.Children) == 0 || len(l.Children[0].Children) == 0 {
		return
	}
	text := l.Children[0].Children[0].Shape
	x, y := text.X-l.Shape.X, text.Y-l.Shape.Y
	if x != ed.comp.TextX || y != ed.comp.TextY {
		ed.comp.TextX, ed.comp.TextY = x, y
		ctx.Window.QueueCommand(func(w *gui.Window) { w.InvalidateLayout() })
	}
}

// completionPopup is the list of candidates under the caret.
func completionPopup(ed *sqlEditor) gui.View {
	t := gui.CurrentTheme()
	c := &ed.comp
	first := min(max(c.Sel-completionRows+1, 0), max(len(c.Items)-completionRows, 0))
	rows := make([]gui.View, 0, completionRows)
	for i := first; i < min(first+completionRows, len(c.Items)); i++ {
		item := c.Items[i]
		color := gui.Color{}
		if i == c.Sel {
			color = t.ColorSelect
		}
		rows = append(rows, gui.Row(gui.ContainerCfg{
			ID:         gui.ScopeIDN(ed.ID, "complete", i),
			Sizing:     gui.FillFit,
			Padding:    gui.PadVH(2, 6),
			Spacing:    gui.SpacingMedium,
			SizeBorder: gui.NoBorder,
			Radius:     gui.RadiusSmall,
			Color:      color,
			Hover:      gui.HoverStyle{Color: t.ColorHover, Cursor: gui.CursorPointingHand},
			OnClick: func(ctx gui.EventCtx) {
				ed.comp.Sel = i
				edit.TriggerAction(ctx.Window, ed.ID, actionCompleteAccept)
				ctx.Window.SetFocus(ed.ID)
				ctx.Consume()
			},
			Content: []gui.View{
				gui.Text(gui.TextCfg{Text: item.Text, TextStyle: t.TextStyleCodeSmall}),
				components.Spacer(),
				gui.Text(gui.TextCfg{Text: item.Kind.String(), TextStyle: t.TextStyleCaption}),
			},
		}))
	}
	return gui.Column(gui.ContainerCfg{
		ID:            ed.ID + "-complete",
		Float:         true,
		FloatAutoFlip: true,
		FloatAnchor:   gui.FloatTopLeft,
		FloatTieOff:   gui.FloatTopLeft,
		FloatOffsetX:  c.TextX + c.X,
		FloatOffsetY:  c.TextY + c.Y,
		MinWidth:      220,
		Padding:       gui.PadAll(2),
		Spacing:       gui.NoSpacing,
		Color:         t.ColorPanel,
		ColorBorder:   t.ColorBorder,
		SizeBorder:    gui.BorderThin,
		Radius:        gui.RadiusSmall,
		Content:       rows,
	})
}
