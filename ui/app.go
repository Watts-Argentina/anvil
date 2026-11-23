package ui

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/Watts-Argentina/anvil/internal/connections"
	"github.com/Watts-Argentina/anvil/internal/drivers"
	"github.com/Watts-Argentina/anvil/internal/history"
	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/internal/session"
	"github.com/Watts-Argentina/anvil/internal/settings"
	"github.com/Watts-Argentina/anvil/models"
	"github.com/Watts-Argentina/anvil/pkg/appErrors"
	"github.com/go-gui-org/go-gui/gui"
)

// App is the main window state. The UI does not talk to the databases or
// the keyring: everything goes through the services.
type App struct {
	Conns    *connections.Service
	Settings *settings.Service
	History  *history.Service
	Sessions *session.Manager

	Prefs       models.Settings
	Connections []models.DBConn
	Selected    uint // connection selected in the sidebar
	Status      string

	// Per open connection.
	Expanded   map[uint]bool
	Connecting map[uint]bool
	// Server databases, for connections with no database configured.
	Databases map[uint][]string

	// Per database (for connections with a configured database, Database is "").
	DBExpanded map[dbKey]bool
	Tables     map[dbKey][]drivers.Table
	// Columns known for autocompletion, keyed by lower-case table name
	// (SPEC §5.1). A nil entry is being loaded or failed to load.
	Columns map[dbKey]map[string][]drivers.Column
	Filter  map[dbKey]string
	Loading map[dbKey]bool

	// Open tabs of the central area, in order, and the active one (SPEC §4.1).
	Tabs      []*tab
	Active    *tab
	tabSeq    int
	TabSel    string // last chosen view (structure, data); new table tabs start on it
	editorSeq int
	Form      connForm
	Pending   string // password in the connect prompt
	NewTable  tableForm
	NewDB     databaseForm
	Export    exportForm

	SidebarRatio  float32
	KeyringWarned bool

	lastClick struct { // for doubleClick
		id string
		at time.Time
	}
}

// dbKey identifies a database within a connection. Database is "" when
// the connection has its database configured.
type dbKey struct {
	ConnID   uint
	Database string
}

// tableView is a table open in a tab.
type tableView struct {
	Key     dbKey
	Table   drivers.Table
	Columns []drivers.Column
	FKs     []drivers.ForeignKey
	// Filter is set when the view was opened by following a foreign key;
	// Back is the view it came from.
	Filter  []query.Cond
	Back    *tableView
	Loading bool
	Err     string
	Data    dataState
}

func newApp(conns *connections.Service, st *settings.Service, h *history.Service) *App {
	return &App{
		Conns:        conns,
		Settings:     st,
		History:      h,
		TabSel:       tabData,
		Sessions:     session.NewManager(),
		Prefs:        settings.Defaults(),
		Expanded:     map[uint]bool{},
		Connecting:   map[uint]bool{},
		Databases:    map[uint][]string{},
		DBExpanded:   map[dbKey]bool{},
		Tables:       map[dbKey][]drivers.Table{},
		Columns:      map[dbKey]map[string][]drivers.Column{},
		Filter:       map[dbKey]string{},
		Loading:      map[dbKey]bool{},
		SidebarRatio: 0.24,
	}
}

func appOf(w *gui.Window) *App { return gui.State[App](w) }

// conn returns the saved connection with that ID.
func (a *App) conn(id uint) (models.DBConn, bool) {
	for _, c := range a.Connections {
		if c.ID == id {
			return c, true
		}
	}
	return models.DBConn{}, false
}

// forgetConn clears the sidebar state of a closed connection.
func (a *App) forgetConn(id uint) {
	delete(a.Expanded, id)
	delete(a.Databases, id)
	for k := range a.Tables {
		if k.ConnID == id {
			delete(a.Tables, k)
			delete(a.Columns, k)
		}
	}
	for k := range a.DBExpanded {
		if k.ConnID == id {
			delete(a.DBExpanded, k)
		}
	}
	for k := range a.Filter {
		if k.ConnID == id {
			delete(a.Filter, k)
		}
	}
	for k := range a.Loading {
		if k.ConnID == id {
			delete(a.Loading, k)
		}
	}
	for _, t := range slices.Clone(a.Tabs) {
		if t.Key.ConnID == id {
			dropTab(a, t)
		}
	}
}

// async runs work off the UI thread and delivers the result to done on the
// UI thread, repainting the window.
func async[T any](w *gui.Window, work func(ctx context.Context) (T, error), done func(w *gui.Window, v T, err error)) {
	go func() {
		v, err := work(context.Background())
		w.QueueCommand(func(w *gui.Window) {
			done(w, v, err)
			w.InvalidateLayout()
		})
	}()
}

// showError shows an error in a dialog with the AppError's title and
// description.
func showError(w *gui.Window, err error) {
	title, body := "Unexpected error.", err.Error()
	var ae *appErrors.AppError
	if errors.As(err, &ae) {
		title, body = ae.Title, ae.Description
	}
	w.Dialog(gui.DialogCfg{
		DialogType: gui.DialogMessage,
		Title:      title,
		Body:       body,
		MaxWidth:   gui.SomeF(520),
	})
}

// showWarning shows a non-blocking warning.
func showWarning(w *gui.Window, err error) {
	title, body := "Warning", err.Error()
	var ae *appErrors.AppError
	if errors.As(err, &ae) {
		title, body = ae.Title, ae.Description
	}
	w.Toast(gui.ToastCfg{Title: title, Body: body, Severity: gui.ToastWarning})
}

// reloadConnections reads the saved connections again.
func reloadConnections(w *gui.Window) {
	a := appOf(w)
	async(w, a.Conns.List, func(w *gui.Window, conns []models.DBConn, err error) {
		if err != nil {
			showError(w, err)
			return
		}
		a := appOf(w)
		a.Connections = conns
		if _, ok := a.conn(a.Selected); !ok {
			a.Selected = 0
			if len(conns) > 0 {
				a.Selected = conns[0].ID
			}
		}
	})
}
