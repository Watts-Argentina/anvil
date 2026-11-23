---
name: anvil-conventions
description: Anvil architecture and code conventions (package layout, appErrors, GORM only for local config, keyring, models, known debt in SPEC §12). Use when starting any task in anvil, when creating a new package under internal/, or when touching internal/connections, models/ or pkg/appErrors.
---

# Anvil conventions

The source of truth is `SPEC.md` at the repo root. If the code and the spec disagree, the spec wins; if the
spec is ambiguous or contradicts a technical constraint, **ask before deciding**.

## Packages (SPEC §2)

```
main.go                 only starts ui.Start()
ui/                     go-gui views; no SQL or direct keyring access (they call services)
ui/components/          reusable components (Icon, IconButton, …)
internal/connections    DBConn CRUD + keyring (Save → SaveResult{Conn, Warning})
internal/storage        opens <UserConfigDir>/anvil/anvil.db and migrates
internal/settings       Settings (single row ID=1)
internal/session        Manager: one *sql.DB per open connection (thread-safe)
internal/drivers        Driver interface per engine (sql-drivers skill)
internal/query          async execution, pagination, split (query-execution skill)
internal/editing        pending changes and DML (grid-editing skill)
internal/schema         DDL: CREATE TABLE / CREATE DATABASE builders and Exec (SPEC §3.5)
internal/history        query history
internal/export         CSV / JSON
internal/mongodb        MongoDB backend (SPEC §2.3): same types as drivers/query/editing; UI branches on Session.IsMongo()
models/                 GORM models for the local config
pkg/appErrors           errors for the UI
assets/                 embedded SVG icons
```

- Services are structs with explicit dependencies (`connections.Service{DB *gorm.DB}`) and methods that
  take `context.Context` first. No global state.
- The UI never imports `database/sql`, drivers or `go-keyring`: it goes through `internal/*`.
- Small files per operation (`save.go`, `delete.go`, `password.go`), as in `internal/connections`.
- **Everything is in English**: code comments, docs, skills, commit messages and user-facing text (no i18n in v1).

## Local persistence (SPEC §2.1, §6.3)

- SQLite at `filepath.Join(os.UserConfigDir(), "anvil", "anvil.db")`, through GORM with a **pure Go** driver
  (`github.com/glebarez/sqlite`), no CGO. Never import `modernc.org/sqlite` (double registration of `"sqlite"`). `AutoMigrate` on startup with `DBConn`, `QueryHistory`, `Settings`.
- GORM **only** for the local config. User databases go through `database/sql` (`sql-drivers` skill).
- Generic GORM API already in use: `gorm.G[models.DBConn](db).Create(ctx, &conn)`. Keep that style.
- GORM's `Delete` does not return `ErrRecordNotFound`: check `RowsAffected`.

## Errors (SPEC §11)

- Every error that crosses into the UI is an `*appErrors.AppError` created with
  `appErrors.Error(title, description, err)`. Short Title, actionable Description, original `err`
  **always** wrapped (never `nil` if there was an error).
- `AppError` must have `Unwrap() error` for `errors.Is/As`.
- Engine errors: include the engine message in the Description or show it separately.

## Passwords (SPEC §3.2)

- Keyring with `github.com/zalando/go-keyring`, service `"ANVIL-DBM"`,
  key `strconv.FormatUint(uint64(conn.ID), 10)`. **Never** `string(conn.ID)` (converts to a rune).
- If the keyring fails or does not exist: do not persist, `SavePassword=false`, ask for the password on
  connect, warn once. Keyring calls may block (unlock prompt): run them off the UI thread.
- Create: create the `DBConn` and store the password consistently (transaction or fallback). Delete: also
  remove the keyring entry (ignore `keyring.ErrNotFound`).

## Known debt (SPEC §12)

The 6 items from the initial review are resolved. See SPEC §12.1 for progress by milestone.

## UI: established patterns

- `ui/app.go`: `App` (window state), `appOf(w)`, `async(w, work, done)` for all I/O (runs in a goroutine and
  comes back with `QueueCommand` + `InvalidateLayout`), `showError` (dialog) and `showWarning` (toast).
- Inside a custom dialog, errors go **inline** (`App.Form.Error`, `dataState.SaveErr`): a new `w.Dialog`
  replaces the open one.
- Central area: `App.Tabs` / `App.Active` (`ui/tabs.go`, SPEC §4.1). A `tab` is a table (`tab.Table` is
  the current `tableView`: columns, FKs, FK filter, `Back` to the view it came from; `tab.Sub` its view) or an
  SQL editor (`tab.SQL`). `a.table()` is the active tab's view. `tableView.Data` is the Data view state
  (page, pending changes, selection). `openTable` (sidebar) activates the table's tab or fills the preview
  tab; `showTable(w, tab, view)`/`followFK` push onto the tab's chain. Pending changes live in their tab:
  closing a tab, disconnecting and anything that drops a view or a page goes through `leaveTable` /
  `leaveTabs` (`grid-editing` skill). Shortcuts are global `gui.Command`s (`registerTabCommands`).
- UI tests in `ui/ui_test.go`: `testApp` builds real state with SQLite; `checkFindings` uses
  `TestFindings(gui.DebugAll)`; `ANVIL_SCREENSHOTS=<dir> go test ./ui/` saves headless PNGs (`soft` backend).

## Definition of done

- `gofmt`, `go vet ./...`, `go build ./...` and `CGO_ENABLED=0 go build ./...` on Linux.
- Tests with `go test ./...`; `internal/*` logic is tested against in-memory SQLite
  (`file::memory:?cache=shared`) and, if Docker is available, against real PG/MySQL:
  ```sh
  docker run -d --rm --name anvil-pg -e POSTGRES_PASSWORD=anvil -p 127.0.0.1:55432:5432 postgres:18-alpine
  docker run -d --rm --name anvil-my -e MYSQL_ROOT_PASSWORD=anvil -e MYSQL_DATABASE=anvil -p 127.0.0.1:53306:3306 mysql:8.4
  ANVIL_TEST_PG="127.0.0.1 55432 postgres anvil postgres" ANVIL_TEST_MYSQL="127.0.0.1 53306 root anvil anvil" go test -count=1 ./...
  ```
  MongoDB: a single-node replica set (transactions) and a standalone server (none). `directConnection=true`
  is required: the replica set member advertises its in-container address.
  ```sh
  docker run -d --rm --name anvil-mongo -p 127.0.0.1:57017:27017 mongo:8 --replSet rs0 --bind_ip_all
  docker exec anvil-mongo mongosh --quiet --eval 'rs.initiate()'
  docker run -d --rm --name anvil-mongo-sa -p 127.0.0.1:57018:27017 mongo:8
  ANVIL_TEST_MONGO="mongodb://127.0.0.1:57017/?directConnection=true" ANVIL_TEST_MONGO_STANDALONE="mongodb://127.0.0.1:57018/" go test -count=1 ./...
  ```
  Packages run in parallel against the **same** server databases: each test uses its own tables and must
  not assume it sees only those (filter listings).
- If the behavior described in `SPEC.md` changes, update the spec in the same change.
