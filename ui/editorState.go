package ui

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/go-gui-org/go-edit/edit"
	"github.com/go-gui-org/go-edit/edit/buffer"
	"github.com/go-gui-org/go-edit/edit/text"
)

// editorFields reads and writes the exported fields of go-edit's editor
// state, whose type is unexported (go-edit v0.12): actions receive it as *S.
type editorFields struct{ v reflect.Value }

func fieldsOf[S any](st *S) editorFields { return editorFields{reflect.ValueOf(st).Elem()} }

func (f editorFields) field(name string) (reflect.Value, bool) {
	v := f.v.FieldByName(name)
	return v, v.IsValid()
}

// cursor returns the primary cursor, nil if there is none. It points into
// the editor state, so changing it moves the cursor.
func (f editorFields) cursor() *edit.CursorState {
	if v, ok := f.field("Cursors"); ok {
		if cs, ok := v.Interface().([]edit.CursorState); ok && len(cs) > 0 {
			return &cs[0]
		}
	}
	return nil
}

func (f editorFields) float(name string) float32 {
	if v, ok := f.field(name); ok && v.CanFloat() {
		return float32(v.Float())
	}
	return 0
}

// caret returns the bottom-left corner of the primary cursor, relative to
// the editor's top-left, as go-edit draws it without line wrap or folding
// (editor_draw.go).
func (f editorFields) caret(buf *buffer.Buffer, lineNumbers bool) (x, y float32, ok bool) {
	c := f.cursor()
	v, found := f.field("Measurer")
	if c == nil || !found {
		return 0, 0, false
	}
	m, _ := v.Interface().(*text.Measurer)
	if m == nil {
		return 0, 0, false
	}
	advance := m.Advance()
	var gutter float32
	if lineNumbers {
		digits := max(len(strconv.Itoa(buf.LineCount())), 3)
		gutter = m.TextWidth(strings.Repeat("0", digits)) + 2*advance
	}
	p := c.Cursor
	x = gutter + advance/2 + m.XForColumn(buf.Line(p.Line), p.ByteCol) - f.float("ScrollX")
	y = float32(p.Line+1)*m.LineHeight() - f.float("ScrollY")
	return x, y, true
}
