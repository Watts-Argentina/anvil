package ui

import (
	"context"
	"path/filepath"

	"github.com/Watts-Argentina/anvil/internal/export"
	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/ui/components"
	"github.com/go-gui-org/go-gui/gui"
)

// exportForm is the state of the Export dialog (SPEC §9).
type exportForm struct {
	Name   string // file name without extension
	Format export.Format
	Full   bool
	Page   query.Result
	// stream writes the full result; nil when it cannot run again (an
	// INSERT … RETURNING).
	stream func(ctx context.Context, sink query.RowSink) error
	Busy   bool
	cancel context.CancelFunc
	Error  string
}

const (
	scopePage = "Current page"
	scopeFull = "Full result"
)

// exportButton opens the Export dialog for a grid.
func exportButton(id string, disabled bool, open func(w *gui.Window)) gui.View {
	return components.IconButton("download", "Export", gui.ButtonCfg{
		ID:       id,
		Variant:  gui.ButtonGhost,
		Disabled: disabled,
		OnClick: func(ctx gui.EventCtx) {
			open(ctx.Window)
			ctx.Consume()
		},
	})
}

func openExport(w *gui.Window, name string, page query.Result, stream func(context.Context, query.RowSink) error) {
	a := appOf(w)
	a.Export = exportForm{Name: name, Format: export.CSV, Full: stream != nil, Page: page, stream: stream}
	w.Dialog(gui.DialogCfg{
		ID:         "export",
		DialogType: gui.DialogCustom,
		Title:      "Export " + components.Ellipsis(name, 20),
		Width:      380,
		CustomView: exportView,
	})
}

func exportView(w *gui.Window) gui.View {
	f := &appOf(w).Export
	scope := scopePage
	if f.Full {
		scope = scopeFull
	}
	scopes := []string{scopePage}
	if f.stream != nil {
		scopes = append(scopes, scopeFull)
	}

	content := []gui.View{
		components.FormField("Format", gui.Select(gui.SelectCfg{
			ID:       "export-format",
			Items:    []string{string(export.CSV), string(export.JSON)},
			Selected: []string{string(f.Format)},
			Disabled: f.Busy,
			OnSelect: func(sel []string, ctx gui.EventCtx) {
				appOf(ctx.Window).Export.Format = export.Format(sel[0])
				ctx.Consume()
			},
		})),
		components.FormField("Rows", gui.Select(gui.SelectCfg{
			ID:       "export-scope",
			Items:    scopes,
			Selected: []string{scope},
			Disabled: f.Busy,
			OnSelect: func(sel []string, ctx gui.EventCtx) {
				appOf(ctx.Window).Export.Full = sel[0] == scopeFull
				ctx.Consume()
			},
		})),
	}
	if f.Busy {
		content = append(content, gui.Text(gui.TextCfg{Text: "Exporting…", TextStyle: gui.CurrentTheme().TextStyleSecondary}))
	}
	if f.Error != "" {
		content = append(content, components.ErrorText(f.Error))
	}
	content = append(content, components.DialogButtons(
		gui.Button(gui.ButtonCfg{
			ID:      "export-cancel",
			Label:   "Cancel",
			Variant: gui.ButtonSecondary,
			OnClick: func(ctx gui.EventCtx) {
				if f.cancel != nil {
					f.cancel() // the export fails, cancelled, and leaves no file
				}
				ctx.Window.DialogDismiss()
				ctx.Consume()
			},
		}),
		gui.Button(gui.ButtonCfg{
			ID:       "export-save",
			Label:    "Export…",
			Variant:  gui.ButtonPrimary,
			Disabled: f.Busy,
			OnClick: func(ctx gui.EventCtx) {
				chooseExportFile(ctx.Window)
				ctx.Consume()
			},
		}),
	))
	return components.DialogBody(content...)
}

// chooseExportFile asks where to save and runs the export.
func chooseExportFile(w *gui.Window) {
	f := appOf(w).Export
	ext := string(f.Format)
	w.NativeSaveDialog(gui.NativeSaveDialogCfg{
		Title:            "Export",
		DefaultName:      f.Name + "." + ext,
		DefaultExtension: ext,
		Filters:          []gui.NativeFileFilter{{Name: ext, Extensions: []string{ext}}},
		ConfirmOverwrite: true,
		OnDone: func(r gui.NativeDialogResult, w *gui.Window) {
			switch paths := r.PathStrings(); {
			case r.Status == gui.DialogError:
				appOf(w).Export.Error = dialogErrText(r)
			case r.Status == gui.DialogOK && len(paths) > 0:
				runExport(w, paths[0])
			}
		},
	})
}

func runExport(w *gui.Window, path string) {
	f := &appOf(w).Export
	if f.Busy {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.Busy, f.cancel, f.Error = true, cancel, ""
	format, full, page, stream := f.Format, f.Full && f.stream != nil, f.Page, f.stream

	async(w, func(context.Context) (int, error) {
		defer cancel()
		return export.ToFile(path, format, func(ew *export.Writer) error {
			if full {
				return stream(ctx, ew)
			}
			return export.Result(page)(ew)
		})
	}, func(w *gui.Window, n int, err error) {
		a := appOf(w)
		a.Export.Busy, a.Export.cancel = false, nil
		if err != nil {
			if ctx.Err() == nil { // a cancelled export closed its dialog already
				a.Export.Error = errText(err)
			}
			return
		}
		w.DialogDismiss()
		a.Status = "Exported " + itoa(n) + " rows to " + filepath.Base(path)
	})
}
