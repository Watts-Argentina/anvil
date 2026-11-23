package ui

import (
	"context"
	"strings"
	"time"

	"github.com/Watts-Argentina/anvil/internal/drivers"
	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/models"
	"github.com/Watts-Argentina/anvil/ui/components"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/go-gui-org/go-edit/edit"
	"github.com/go-gui-org/go-edit/edit/buffer"
	"github.com/go-gui-org/go-edit/edit/highlight"
	"github.com/go-gui-org/go-gui/gui"
)

// actionRun is the editor action that runs the selection or the statement
// under the cursor (SPEC §5.2).
const actionRun = "anvil.run"

// sqlEditor is an SQL editor on a database, with its last result.
type sqlEditor struct {
	ID      string
	Key     dbKey
	Buf     *buffer.Buffer
	hl      *highlight.Highlighter
	hlTheme string

	Running bool
	cancel  context.CancelFunc

	Run         *query.RunResult
	Page        *query.RowsPage
	PageLoading bool
	Err         string

	comp completionState

	// Sizes measured in the previous frame: go-edit needs a fixed width
	// and height, and the grid a bounded height.
	editorSize components.BoxSize
	gridSize   components.BoxSize
	bodySize   components.BoxSize
}

func (ed *sqlEditor) close() {
	if ed.cancel != nil {
		ed.cancel()
	}
	if ed.hl != nil {
		ed.hl.Close()
	}
}

// newEditor creates an SQL editor on a database.
func newEditor(a *App, key dbKey) *sqlEditor {
	a.editorSeq++
	ed := &sqlEditor{ID: gui.ScopeIDN("sql-editor", "", a.editorSeq), Key: key, Buf: buffer.New()}
	ed.watchEdits()
	return ed
}

// editorByID finds the editor of an SQL tab by its widget ID.
func editorByID(a *App, id string) *sqlEditor {
	for _, t := range a.Tabs {
		if t.SQL != nil && t.SQL.ID == id {
			return t.SQL
		}
	}
	return nil
}

// highlighter returns the editor's highlighter for the current theme.
func (ed *sqlEditor) highlighter(a *App) *highlight.Highlighter {
	// The chroma style depends on the theme: it is recreated when it changes.
	if ed.hl == nil || ed.hlTheme != a.Prefs.Theme {
		if ed.hl != nil {
			ed.hl.Close()
		}
		style := styles.Get("github-dark")
		if a.Prefs.Theme == models.ThemeLight {
			style = styles.Get("github")
		}
		ed.hl = highlight.New(ed.Buf, lexerFor(a, ed.Key), style)
		ed.hlTheme = a.Prefs.Theme
	}
	return ed.hl
}

// lexerFor picks the chroma lexer for the engine (SPEC §5.1).
func lexerFor(a *App, key dbKey) string {
	c, _ := a.conn(key.ConnID)
	switch c.Engine {
	case models.EnginePostgres:
		return "postgresql"
	case models.EngineMySQL:
		return "mysql"
	case models.EngineMSSQL:
		return "tsql"
	}
	return "sql"
}

// textOffset converts a buffer position into a byte offset in text.
func textOffset(text string, p buffer.Position) int {
	off := 0
	for range p.Line {
		i := strings.IndexByte(text[off:], '\n')
		if i < 0 {
			return len(text)
		}
		off += i + 1
	}
	return min(off+p.ByteCol, len(text))
}

func sqlTab(w *gui.Window, a *App, ed *sqlEditor) gui.View {
	t := gui.CurrentTheme()
	hl := ed.highlighter(a)
	syncCompletion(w, ed)

	runBtn := components.IconButton("play", "Run", gui.ButtonCfg{
		ID:       "sql-run",
		Variant:  gui.ButtonPrimary,
		Disabled: ed.Running,
		OnClick: func(ctx gui.EventCtx) {
			// The action reads the editor's selection and cursor.
			edit.TriggerAction(ctx.Window, ed.ID, actionRun)
			ctx.Consume()
		},
	})
	stopBtn := components.IconButton("stop", "Stop", gui.ButtonCfg{
		ID:       "sql-stop",
		Disabled: !ed.Running,
		OnClick: func(ctx gui.EventCtx) {
			if ed.cancel != nil {
				ed.cancel()
			}
			ctx.Consume()
		},
	})

	toolbar := components.Toolbar("",
		runBtn,
		stopBtn,
		gui.Text(gui.TextCfg{Text: "Ctrl+Enter runs the selection or the statement under the cursor", TextStyle: t.TextStyleCaption}),
		components.Spacer(),
		components.Icon("database", 12),
		gui.Text(gui.TextCfg{Text: dbLabel(a, ed.Key), TextStyle: t.TextStyleCaption}),
	)

	actions := completionActions()
	actions[actionRun] = edit.Action{ID: actionRun, PreservesAnchor: true, Execute: runAction}
	editor := edit.Editor(edit.EditorCfg{
		ID:               ed.ID,
		Buffer:           ed.Buf,
		Width:            ed.editorSize.W,
		Height:           ed.editorSize.H,
		ShowLineNumbers:  true,
		ShowBracketMatch: true,
		Decorations:      []edit.DecorationProvider{hl},
		Keymaps: []*edit.Keymap{{
			Name: "anvil",
			Bindings: []edit.Binding{
				{Key: gui.KeyEnter, Modifiers: gui.ModCtrl, ActionID: actionRun},
				{Key: gui.KeyEnter, Modifiers: gui.ModSuper, ActionID: actionRun},
			},
		}, completionKeymap(ed.comp.Open)},
		Actions: actions,
	})
	pane := []gui.View{editor}
	if ed.comp.Open {
		pane = append(pane, completionPopup(ed))
	}

	return gui.Column(gui.ContainerCfg{
		ID:         "sql",
		Sizing:     gui.FillFill,
		Padding:    gui.NoPadding,
		Spacing:    gui.SpacingSmall,
		SizeBorder: gui.NoBorder,
		Content: []gui.View{
			toolbar,
			// No gui.Splitter: nested inside the sidebar splitter it doubles the
			// row height of gui.Table (go-gui v0.92).
			components.MeasuredBox("sql-body", &ed.bodySize, false, gui.Column(gui.ContainerCfg{
				Sizing:     gui.FillFill,
				Padding:    gui.NoPadding,
				Spacing:    gui.SpacingSmall,
				SizeBorder: gui.NoBorder,
				Content: []gui.View{
					gui.Column(gui.ContainerCfg{
						Height:     editorHeight(ed),
						Sizing:     gui.FillFixed,
						Padding:    gui.NoPadding,
						SizeBorder: gui.NoBorder,
						Content: []gui.View{components.MeasuredBox(ed.ID+"-pane", &ed.editorSize, true, gui.Column(gui.ContainerCfg{
							Padding:     gui.NoPadding,
							SizeBorder:  gui.NoBorder,
							AmendLayout: ed.measureText,
							Content:     pane,
						}))},
					}),
					sqlResults(ed),
				},
			})),
		},
	})
}

// editorHeight is 45 % of the editor and results area, with a minimum.
func editorHeight(ed *sqlEditor) float32 {
	return max(ed.bodySize.H*0.45, 120)
}

// runAction is the editor's "anvil.run" action. ActionFunc receives the
// editor state as *editorState, which go-edit does not export: the type
// parameter S is inferred when runAction is assigned to edit.ActionFunc and
// the cursor is read through reflection (editorFields).
func runAction[S any](cfg edit.EditorCfg, st *S, buf *buffer.Buffer, w *gui.Window) {
	var cursor *edit.CursorState
	if c := fieldsOf(st).cursor(); c != nil {
		cp := *c
		cursor = &cp
	}

	text := buf.String()
	w.QueueCommand(func(w *gui.Window) {
		a := appOf(w)
		ed := editorByID(a, cfg.ID)
		if ed == nil {
			return
		}
		sql := text
		switch {
		case cursor == nil:
		case cursor.HasSelection():
			sql = buf.TextInRange(cursor.SelectionRange())
		default:
			sql = statementAt(a, ed.Key, text, textOffset(text, cursor.Cursor))
		}
		runSQL(w, ed, sql)
	})
}

// statementAt returns the statement under the cursor (SPEC §5.2).
func statementAt(a *App, key dbKey, text string, offset int) string {
	s, ok := a.Sessions.Get(key.ConnID)
	if !ok {
		return text
	}
	span, ok := drivers.StatementAt(s.Driver.SplitStatements(text), offset)
	if !ok {
		return ""
	}
	return span.Text
}

// runSQL builds the plan, asks for confirmation if there are warnings and runs it.
func runSQL(w *gui.Window, ed *sqlEditor, sql string) {
	a := appOf(w)
	s, ok := a.Sessions.Get(ed.Key.ConnID)
	if !ok || ed.Running {
		return
	}

	plan, err := query.NewPlan(s.Driver, sql, s.Conn.ReadOnly)
	if err != nil {
		ed.Err, ed.Run, ed.Page = errText(err), nil, nil
		return
	}
	if len(plan.Warnings) == 0 {
		executePlan(w, ed, plan, sql)
		return
	}
	w.Dialog(gui.DialogCfg{
		DialogType: gui.DialogConfirm,
		Title:      "Run anyway?",
		Body:       "• " + strings.Join(plan.Warnings, "\n• "),
		MaxWidth:   gui.SomeF(560),
		OnOkYes: func(w *gui.Window) {
			executePlan(w, ed, plan, sql)
		},
	})
}

func executePlan(w *gui.Window, ed *sqlEditor, plan query.Plan, sql string) {
	a := appOf(w)
	key := ed.Key
	s, ok := a.Sessions.Get(key.ConnID)
	if !ok {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	ed.Running, ed.cancel, ed.Err = true, cancel, ""
	a.Status = "Running…"
	size := a.Prefs.DefaultPageSize

	go func() {
		var res query.RunResult
		db, err := s.Pool(ctx, key.Database)
		if err == nil {
			res, err = query.Run(ctx, db, plan, size)
		}
		w.QueueCommand(func(w *gui.Window) {
			cancel()
			ed.Running, ed.cancel = false, nil
			ed.Run, ed.Page = &res, res.Rows
			if err != nil {
				ed.Err = errText(err)
			}
			appOf(w).Status = runSummary(res, err)
			saveHistory(w, key.ConnID, sql, res, err)
			w.InvalidateLayout()
		})
	}()
}

// runSummary summarizes each statement for the status bar (SPEC §5.2).
func runSummary(res query.RunResult, err error) string {
	parts := make([]string, 0, len(res.Statements))
	for i, s := range res.Statements {
		if len(res.Statements) > 1 {
			parts = append(parts, itoa(i+1)+": "+s.Summary())
		} else {
			parts = append(parts, s.Summary())
		}
	}
	out := strings.Join(parts, " | ")
	if res.RolledBack {
		out += " | open transaction rolled back"
	}
	if err != nil && out == "" {
		out = "Query failed"
	}
	return out
}

func saveHistory(w *gui.Window, connID uint, sql string, res query.RunResult, runErr error) {
	a := appOf(w)
	h := models.QueryHistory{
		ConnID:       connID,
		SQL:          sql,
		Duration:     res.Duration.Milliseconds(),
		RowsAffected: res.Affected(),
	}
	if runErr != nil {
		h.Error = errText(runErr)
	}
	async(w, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, a.History.Add(ctx, h)
	}, func(w *gui.Window, _ struct{}, err error) {
		if err != nil {
			showWarning(w, err)
		}
	})
}

func loadResultPage(w *gui.Window, ed *sqlEditor, index int) {
	a := appOf(w)
	key := ed.Key
	s, ok := a.Sessions.Get(key.ConnID)
	if !ok || ed.Page == nil || ed.PageLoading {
		return
	}
	ed.PageLoading = true
	page := *ed.Page

	async(w, func(ctx context.Context) (query.RowsPage, error) {
		db, err := s.Pool(ctx, key.Database)
		if err != nil {
			return page, err
		}
		return query.LoadRowsPage(ctx, db, page, index)
	}, func(w *gui.Window, p query.RowsPage, err error) {
		ed.PageLoading = false
		if err != nil {
			showError(w, err)
			return
		}
		ed.Page = &p
	})
}

func sqlResults(ed *sqlEditor) gui.View {
	t := gui.CurrentTheme()
	var content []gui.View

	switch {
	case ed.Running:
		content = append(content, gui.Text(gui.TextCfg{Text: "Running…", TextStyle: t.TextStyleSecondary}))
	case ed.Err != "":
		content = append(content, components.ErrorText(ed.Err))
	}

	if !ed.Running && ed.Page != nil {
		p := ed.Page
		first := p.Index*p.Size + 1
		info := "No rows"
		if len(p.Rows) > 0 {
			info = "Rows " + itoa(first) + "–" + itoa(first+len(p.Rows)-1)
		}
		content = append(content,
			gui.Row(gui.ContainerCfg{
				Padding:    gui.NoPadding,
				Spacing:    gui.SpacingSmall,
				SizeBorder: gui.NoBorder,
				VAlign:     gui.VAlignMiddle,
				Content: []gui.View{
					components.IconButton("chevronLeft", "", gui.ButtonCfg{
						ID:       "sql-prev",
						A11YCfg:  gui.A11YCfg{A11YLabel: "Previous page"},
						Disabled: ed.PageLoading || p.Index == 0 || !p.Repeatable,
						OnClick: func(ctx gui.EventCtx) {
							loadResultPage(ctx.Window, ed, p.Index-1)
							ctx.Consume()
						},
					}),
					components.IconButton("chevronRight", "", gui.ButtonCfg{
						ID:       "sql-next",
						A11YCfg:  gui.A11YCfg{A11YLabel: "Next page"},
						Disabled: ed.PageLoading || !p.HasNext || !p.Repeatable,
						OnClick: func(ctx gui.EventCtx) {
							loadResultPage(ctx.Window, ed, p.Index+1)
							ctx.Consume()
						},
					}),
					gui.Text(gui.TextCfg{Text: info, TextStyle: t.TextStyleCaption}),
				},
			}),
			resultGrid("sql-grid", p.Result, &ed.gridSize),
		)
	} else if !ed.Running && ed.Run != nil && ed.Err == "" {
		var affected int64
		var d time.Duration
		for _, s := range ed.Run.Statements {
			affected += s.Affected
			d += s.Duration
		}
		msg := "Statement executed · " + itoa(int(affected)) + " rows affected · " + d.Round(time.Millisecond).String()
		content = append(content, gui.Text(gui.TextCfg{Text: msg, TextStyle: t.TextStyleSuccess}))
	}

	if len(content) == 0 {
		content = append(content, gui.Text(gui.TextCfg{Text: "Results appear here.", TextStyle: t.TextStyleSecondary}))
	}

	return gui.Column(gui.ContainerCfg{
		ID:         "sql-results",
		Sizing:     gui.FillFill,
		Padding:    gui.PadVH(4, 0),
		Spacing:    gui.SpacingSmall,
		SizeBorder: gui.NoBorder,
		Content:    content,
	})
}
