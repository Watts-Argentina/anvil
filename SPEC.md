# Anvil DBM — Specification v1

## 1. Vision

Anvil is a free (FOSS), cross-platform desktop database manager written in Go with
[go-gui](https://github.com/go-gui-org/go-gui). It is aimed at technical users in general:
connect to a database, explore its tables, run SQL and edit data safely.

| Aspect      | Decision                                   |
|-------------|--------------------------------------------|
| Users       | General FOSS audience (technical users)    |
| Platforms   | Linux (X11/Wayland), Windows, macOS        |
| Engines     | SQLite, PostgreSQL, MySQL / MariaDB, SQL Server, Oracle 12c+, MongoDB (§2.3) |

### 1.1 v1 scope

Included:
- Connection management (create, edit, delete, test, connect/disconnect).
- Simple list of tables and views per connection, with a refresh button per connection and database.
- Tabs in the central area: tables and SQL editors, from any connection (§4.1).
- Creating tables (with a form) and databases (§3.5).
- SQL editor with highlighting, autocompletion and history.
- Paginated results grid.
- Data editing in the grid, with pending changes and a transaction.
- Export of results to CSV/JSON.
- Light/dark theme.

Out of scope for v1:
- Full schema explorer (indexes, triggers, altering or dropping objects). Foreign keys are only read to navigate between rows (§6.5).
- SSH tunnel and connection folders/groups.
- Specialized JSON/JSONB editor (shown and edited as text).
- i18n (the UI stays in English) and configurable shortcuts.
- Editing the results of arbitrary queries.

---

## 2. Architecture

```
main.go
ui/                  go-gui views (layout, modals, grid and its editing, SQL editor)
ui/components/       reusable components (IconButton, Icon, …)
internal/connections connection CRUD service + keyring
internal/storage     opening and migrating the local database
internal/settings    local preferences (single row)
internal/session     open connections: one *sql.DB per connection
internal/drivers     per-engine abstraction: connection, dialect, metadata, split and analysis
internal/query       async execution, pagination (with FK filter), cancellation
internal/editing     pending changes, validation, DML generation and the save transaction
internal/schema      DDL to create tables and databases
internal/history     query history
internal/completion  SQL editor autocompletion: keywords per engine, word at cursor, candidates
internal/export      CSV / JSON writers and atomic file write
internal/mongodb     MongoDB backend: collections, document pages, editing by _id, commands (§2.3)
models/              GORM models for the local configuration
pkg/appErrors        errors with Title/Description to show in the UI
assets/              own embedded SVG icons
```

### 2.1 Local persistence

- SQLite at `os.UserConfigDir()/anvil/anvil.db` (for example `~/.config/anvil/anvil.db`), accessed with GORM.
- **Pure Go** SQLite driver (`glebarez/sqlite` for GORM, which uses `glebarez/go-sqlite`, a fork of
  `modernc.org/sqlite`) to build without CGO on the 3 platforms. The same driver (`glebarez/go-sqlite`, name
  `"sqlite"`) is used for the user's SQLite connections. **Do not** import `modernc.org/sqlite` directly:
  both register `"sqlite"` and the program panics on startup.
- `AutoMigrate` on startup, with the models from §3 and §6.3.

### 2.2 Access to the user's databases

- GORM is used **only** for the local configuration. The user's databases are queried with `database/sql`,
  because columns and types are dynamic.
- Drivers (all pure Go): `jackc/pgx/v5/stdlib`, `go-sql-driver/mysql`, `glebarez/go-sqlite`,
  `microsoft/go-mssqldb`, `sijms/go-ora/v2`.
- The user's SQLite databases are opened with a `file:` URI (`mode=rw`, or `mode=ro` if read-only), so
  no empty file is created if the path does not exist.
- `Driver` interface per engine:
  - `Open(ctx, DBConn, password) (*sql.DB, error)`
  - `ListDatabases(ctx) ([]string, error)` and `MaintenanceDB() string`: for connections without a database (§3.4).
  - `ListTables(ctx) ([]Table, error)`: tables and views.
  - `Columns(ctx, table) ([]Column, error)`: name, type, nullable, whether it is a PK, whether it is identity/autoincrement.
  - `ForeignKeys(ctx, table) ([]ForeignKey, error)`: name, columns, referenced table and columns (in order).
  - `QuoteIdent(string) string`: `"x"` in PG/SQLite/Oracle, `` `x` `` in MySQL, `[x]` in SQL Server.
  - `Placeholder(n int) string`: `$n` in PG, `?` in MySQL/SQLite, `@pN` in SQL Server, `:N` in Oracle.
  - `SplitStatements(sql string) []Span`: respects strings, comments, `$$` (PG), `DELIMITER` (MySQL),
    `GO` batches and routine bodies (SQL Server), `/` after PL/SQL blocks and `q'[…]'` strings (Oracle).
  - `Analyze(stmt string) Statement`: kind of statement (read/write, DML, `WHERE`, transaction control) for
    the safeguards (§8).
  - `AutoIncrement(type) (def, inlinePK)` and `CreateDatabase(name) string`: the DDL dialect (§3.5).
  - `Limit(orderBy, limit, offset) string`: `LIMIT … OFFSET …` in PG/MySQL/SQLite, `OFFSET … ROWS FETCH
    NEXT … ROWS ONLY` in SQL Server (which adds `ORDER BY (SELECT NULL)` if there is no order) and Oracle.
  - `InsertDefaults(table, cols) string`: the `INSERT` of a row with only defaults (`DEFAULT VALUES`,
    `() VALUES ()` in MySQL, `(first column) VALUES (DEFAULT)` in Oracle).
- MySQL connections use `ClientFoundRows` so that `RowsAffected` counts the rows found, not only the changed
  ones (§7.3).

### 2.3 MongoDB

MongoDB is not SQL, so it does not implement `Driver`: `internal/mongodb` uses the official driver
(`go.mongodb.org/mongo-driver/v2`, pure Go) and returns the same types as the SQL packages (`drivers.Table`,
`drivers.Column`, `query.Page`, `query.RunResult`, `editing.Change`), so the sidebar, tabs, grid, editing and
export are shared. A MongoDB `Session` has `Mongo` (one client for every database) instead of `Driver`/`DB`;
the UI branches with `Session.IsMongo()`.

- **Connection**: `Host` is a host name or a full connection string (`mongodb://…`, `mongodb+srv://…` for
  Atlas; its options, such as `authSource`, are kept). `User` is optional: without it no password is asked.
  SSL modes: `disable` (the connection string decides), `require` (TLS without verifying), `verify-full` (with
  `SSLCA`; `SSLCert`/`SSLKey` for client certificates, the key may be in the same PEM).
- **Databases and collections** are listed like databases and tables (views are read-only). Without `DB`,
  every database the user can read is shown. There are no New table / New database forms (MongoDB creates
  them on the first insert) and no foreign keys.
- **Fields**: a collection has no schema, so its columns are the top-level fields of a sample of 100
  documents: `_id` first (the primary key), then by appearance; the type is the most common BSON type
  (`string`, `int`, `long`, `double`, `decimal`, `bool`, `objectId`, `datetimetz`, `object`, `array`,
  `binary`…). A page also shows the fields of its documents that the sample missed. A missing field is NULL.
- **Values**: scalars as text, dates in UTC (edited with the calendar, keeping the offset), binary read-only,
  documents and arrays as relaxed Extended JSON. An edited cell is saved with the type the value had (or the
  column's for empty cells and new documents); text that does not parse as that type is rejected before
  saving.
- **Pages** in `_id` order with skip/limit; Count with `countDocuments`.
- **Documents view** (the default in the Data tab, `ui/docView.go`): one card per document with a field per
  line (name, value, type); fields the document does not have are not listed, and a new document lists every
  known field, unset. Objects and arrays expand to show their fields, read-only (an open one shows its size,
  `{2 fields}` / `[3 items]`). Top-level fields are edited on click like grid cells; the card number selects
  the document (Ctrl/Shift to add or extend). A **Documents | Table** switch shows the grid instead; both
  share the page, the pending changes, the selection and the edit bar. Expanded fields are per page.

---

## 3. Connection management

### 3.1 `DBConn` model

| Field        | Type     | Notes                                                       |
|--------------|----------|-------------------------------------------------------------|
| `gorm.Model` |          | ID, timestamps, soft delete                                 |
| `Name`       | string   | Visible alias, required                                     |
| `Color`      | string   | Hex (`#RRGGBB`) for the indicator in the sidebar            |
| `Engine`     | string   | `sqlite` \| `postgres` \| `mysql` \| `mssql` \| `oracle` \| `mongodb` |
| `Host`       | string   | Not applicable to SQLite; in MongoDB it may be a connection string (§2.3) |
| `Port`       | int      | Defaults to 5432 / 3306 / 1433 / 1521 / 27017               |
| `User`       | string   | Not applicable to SQLite; optional in MongoDB               |
| `DB`         | string   | Database name; for SQLite it is the file path, for Oracle the service name (required). Empty in PG/MySQL/SQL Server = all databases (§3.4) |
| `ReadOnly`   | bool     | Blocks writes (§7)                                          |
| `SSLMode`    | string   | PG: `disable/require/verify-ca/verify-full`; MySQL: `false/true/skip-verify/custom`; SQL Server: `false/true/trust/strict/disable`; Oracle: `disable/require/verify`; MongoDB: `disable/require/verify-full` |
| `SSLCA`      | string   | Path to the CA                                              |
| `SSLCert`    | string   | Path to the client certificate                              |
| `SSLKey`     | string   | Path to the client key                                      |
| `SavePassword` | bool   | If false, the password is asked for on connect              |

### 3.2 Passwords

- Stored in the OS keyring (service `ANVIL-DBM`, key = `strconv.FormatUint(ID, 10)`).
- If the keyring is not available (for example Linux without Secret Service): **it is not persisted**.
  `SavePassword=false` is set and the password is asked for on every connection. The user is warned once.
- Deleting a connection also deletes its keyring entry.
- The password stays in memory only while the connection is open.

### 3.3 Operations

- **Create and edit**: modal with the fields for the engine (SQLite shows a file picker instead of
  host/port/user). **Test connection** button (Open + Ping with a 10 s timeout).
- **Delete**: confirmation, then deletion of the record and the keyring entry.
- **Connect/disconnect**: from the sidebar. One `*sql.DB` is kept per open connection.

### 3.4 Connection without a database (all databases)

- If the `DB` field is left empty in PostgreSQL, MySQL or SQL Server, the connection shows **every database
  on the server** the user can access, and each database expands to show its tables.
  - PG: `pg_database` without templates, with `datallowconn` and the `CONNECT` privilege. The main connection
    goes to the `postgres` maintenance database (without a database, libpq would use one named after the user).
  - MySQL: `information_schema.SCHEMATA`, hiding `information_schema`, `performance_schema`, `mysql` and `sys`.
  - SQL Server: `sys.databases` without the system ones, online and with `HAS_DBACCESS`. The main connection
    goes to `master`.
  - Oracle has no list: the service is the database and the tables are those of the session's schema.
- Each database is opened in its own pool the first time it is expanded (`Session.Pool`), with the password
  that stays in memory while the connection is open. Every pool is closed on disconnect.

### 3.5 Creating tables and databases

- **New table**: a `+` button next to the table filter of each database opens a dialog with the table name
  and its columns (name, type, Nullable, PK, Auto inc., default). It is disabled on read-only connections.
  The dialog shows the `CREATE TABLE` that will run, built live; Create stays disabled while the definition
  is invalid and the reason is shown instead of the SQL. Name and type have a fixed width so the rows line
  up; past a few columns the list scrolls inside the dialog instead of growing it.
  - Names are quoted with `QuoteIdent`. Types are typed by the user and must be a type name with optional
    arguments (`integer`, `varchar(255)`, `numeric(10, 2)`, `timestamp with time zone`, `text[]`). Defaults
    are an SQL expression (`'abc'`, `0`, `CURRENT_TIMESTAMP`) and cannot end the statement.
  - PK columns are `NOT NULL` and the PK is a table constraint. Auto increment needs the column to be the
    only PK column and have no default: PG and Oracle `GENERATED BY DEFAULT AS IDENTITY`, MySQL
    `AUTO_INCREMENT`, SQL Server `IDENTITY(1,1)`, SQLite `INTEGER PRIMARY KEY AUTOINCREMENT` (the declared
    type is replaced). `DEFAULT` goes before `NOT NULL` (Oracle requires that order).
  - In PG the table goes to the first schema of the `search_path` (usually `public`). After creating it,
    the database's table list is reloaded and the new table opens. Errors stay in the dialog.
- **New database** (PG, MySQL and SQL Server): a **New database** button at the top of the database list of a connection
  without a database (§3.4) asks for the name and runs `CREATE DATABASE` on the main pool. The list is
  reloaded and the new database expands. Disabled on read-only connections.
- **New SQLite file**: the connection form has **New…** next to Browse…, which picks a path with the save
  dialog and creates an empty database file there. An existing file is never overwritten: it is used as is.

---

## 4. Layout

```
┌─ [+Add] [Edit] [Delete] [Disconnect] ────────────────────────────────── [☀] ┐
│ ● prod-pg PG⟳│ ● users • × │ ● SQL · shop × │ ● orders ×                [<>]│
│ [Filter…] [+]│ ‹ users                                                      │
│              │ [Structure] [Data]                                           │
│   users      │ ⟳ ‹ Page 1 › [100▾] rows per page     Count rows             │
│   orders     │  # │ id │ name  │ email   │ customer_id                      │
│ ● local.db   │  + │    │ Dora  │ DEFAULT │ DEFAULT                          │
│              │  1 │ 1  │ Ana   │ a@x.com │ 3 [→]                            │
│              │  2 │ 2  │ Bruno │ NULL    │ 1 [→]                            │
│              │                                                              │
│              │ [Save (2)] [Cancel] │ [Add] [Delete] │ [Set NULL] [Set Now]  │
└──────────────┴──────────────────────────────────────────────────────────────┘
 status bar
```

- **Top bar**: global actions (add, edit and delete connections, connect/disconnect, theme).
- **Sidebar**: connections with their color dot. Expanding an open connection shows a flat list of
  tables and views, with a text filter and the New table button (§3.5). If the connection has no database
  (§3.4), the databases are listed first, after a New database button, and each one expands into its table
  list and its filter. The engine is shown at the right end of the connection row.
- **Refresh**: an open connection has a refresh button (`⟳`) at the end of its row. With a database configured
  it reloads the table list; without one (§3.4) it reloads the database list and the tables of every database
  already loaded (a database that is gone is dropped from the tree). Each loaded database has its own refresh
  button that reloads its tables. The button is disabled while that list is loading; open tabs are not
  touched. The sidebar only scrolls
  vertically: a name longer than the row is clipped, so it never widens the tree or hides the buttons.
- **Central area**: a tab bar (§4.1) above the active tab. A table tab shows the table with two views:
  - **Structure**: columns with type, nullable, PK/autoincrement and FK.
  - **Data**: paginated grid of the table (§6), with foreign key navigation (§6.5) and editing (§7) through
    the bar below the grid. In MongoDB a **Documents | Table** switch under the toolbar shows the documents
    as cards (the default) or as the grid (§2.3).
  A new table tab starts on the last view chosen. A table opened by following a FK has a back button
  (`‹`) next to its name.
- **Status bar**: active connection (the active tab's), query status, time, rows, pagination.
- **Dialogs** never grow past their width or the window: user values in titles (connection or column names)
  are cut with `…`, messages wrap and long lists scroll inside the dialog. A UI test opens every dialog with
  long values and fails if anything sticks out (`TestNoOverflow`).

### 4.1 Tabs

- A tab is an open **table** (with its Structure / Data views and its FK chain, §6.5) or an **SQL
  editor**. Tabs from different connections and databases live in the same bar: each one shows the color dot
  of its connection, an icon (table, view or SQL), its title (the table, schema-qualified like the sidebar,
  or `SQL · <database>`) and a close button (`×`). The bar scrolls horizontally when the tabs do not fit.
- **Preview tab**: a click on a table in the sidebar activates its tab if it is already open (a tab showing that
  table without a FK filter); otherwise it opens it in the **preview** tab (title in italics), which the next
  table opened from the sidebar replaces. A double click on the table or on the tab, following a foreign key
  or having pending changes makes it permanent. A table created with New table (§3.5) opens in a permanent tab.
- **SQL tabs**: the `<>` button at the end of the bar (or Ctrl+T) opens an SQL editor on the active tab's database
  (or the selected connection's, if it has its database configured). Each SQL tab has its own text and result.
  SQL is only written in SQL tabs: table tabs have no SQL view.
- **Pending changes stay in their tab**: switching tabs does not ask (the cell being edited is committed first;
  if its value is invalid, the switch does not happen). A tab with unsaved changes shows `•` after its title.
  Closing it asks first (§7.2).
- Closing the active tab activates its neighbor (the next one, or the previous one if it was the last).
  Disconnecting or deleting a connection asks for each of its tabs with pending changes and then closes all of
  its tabs.
- **Shortcuts** (global: they work with the focus in the editor or the grid, not while a dialog is open):
  Ctrl+W closes the active tab, Ctrl+Tab / Ctrl+Shift+Tab move to the next / previous tab, Ctrl+T opens an
  SQL tab.
- Open tabs are not restored when Anvil starts again (left for later).

---

## 5. SQL editor

### 5.1 Features

- The SQL editor lives in SQL tabs (§4.1): any number of them per database, each with its own text and result.
- Editor: `github.com/go-gui-org/go-edit` (pure Go; selection, undo, line numbers).
- **Syntax highlighting** with `chroma` through go-edit (lexer by engine: `postgresql`, `mysql`, `tsql`, `sql`;
  `github-dark` / `github` style depending on the theme).
- **Autocompletion** (`internal/completion`, `ui/completion.go`): dialect keywords, table and column names
  of the editor's database. Tables come from the sidebar list; columns are loaded in the background for the
  tables mentioned in the text (or a table just completed) and cached per database. The sidebar refresh
  clears that cache. A popup under the cursor opens while typing an identifier or after `table.`
  (`schema.` suggests tables, an unknown qualifier is taken as an alias and suggests every known column);
  `Ctrl+Space` opens it on demand. Up/Down choose, Enter/Tab/click insert, Escape or moving the cursor
  closes it. Keywords keep the case of what was typed; identifiers are inserted unquoted.
- **History** per connection, persisted locally (§6.3), with search and the option to load a query back
  into the editor.

### 5.2 Execution

- `Ctrl+Enter` runs the **selection**; if there is no selection, it runs the **statement under the cursor**
  (determined with `SplitStatements`).
- **Several statements** in the selection: they run in order, in the same session.
  - If several return rows (for example 2 SELECTs), only the **last** result is shown. The status bar
    keeps the summary of each statement (rows or affected rows, and duration).
  - If any statement is an `UPDATE`/`DELETE` (or `INSERT`) and is not inside an explicit transaction
    (`BEGIN … COMMIT/ROLLBACK`), a **warning** is shown before running ("modifies data without a
    transaction") and the user chooses whether to continue or cancel.
  - If a statement fails, execution stops and the app reports which one failed and which statements had
    already run.
- Execution is **asynchronous**: it runs in a goroutine with a `context.WithCancel`. The UI does not block and
  the **Stop** button cancels the query (cancels the context; in PG it sends the driver's cancel request).
- If the statement returns rows, they are shown in the results grid (**read-only**, §6.4). Otherwise,
  the affected rows and the duration are shown.
- Errors are shown with `appErrors` (title and description, plus the engine message).

### 5.3 MongoDB commands

- In a MongoDB connection the editor runs one **database command** written as relaxed Extended JSON, whose
  first key is the command: `{"find": "users", "filter": {"age": {"$gt": 30}}, "sort": {"_id": 1}}`. The
  selection, or else the whole editor, is the command. Highlighting uses the `json` lexer; autocompletion
  offers collections and fields, no keywords.
- `find`, `aggregate`, `listCollections` and `listIndexes` give paginated documents (pages are read again
  from the cursor). `insert`, `update` and `delete` give the affected documents (`n`; write errors fail the
  command). Any other command shows its reply as one row.
- Read-only connections accept only read commands (`find`, `aggregate` without `$out`/`$merge`, `count`,
  `distinct`, `list*`, `*Stats`, `explain`…). An `update` or `delete` with an empty filter asks for
  confirmation (§8). The mongosh syntax (`db.users.find(…)`) is not supported.

---

## 6. Grid and pagination

### 6.1 Page size

- Selectable between **20 and 500** rows (100 by default). Remembered per tab.

### 6.2 Pagination strategy

- **Keyset** if the table has a single PK with identity/autoincrement (or a single sortable PK):
  `SELECT … WHERE pk > :last ORDER BY pk LIMIT :n` (and `<` with `ORDER BY pk DESC` to go back).
- **OFFSET** in every other case: `SELECT … ORDER BY <pk or nothing> LIMIT :n OFFSET :k`.
- SQL editor results are paginated by wrapping the user's query:
  `SELECT * FROM (<query>) AS q LIMIT :n OFFSET :k`. This only applies to SELECT/WITH statements; everything
  else runs as is.
- The total row count (`COUNT(*)`) is computed on demand (button), not on every page.

### 6.3 Additional local models

- `QueryHistory { gorm.Model; ConnID uint; SQL string; Duration int64; RowsAffected int64; Error string }`.
- `Settings { Theme string; DefaultPageSize int }` (single row).

### 6.4 Data types

| Type               | Display                               | Editing                     |
|--------------------|---------------------------------------|-----------------------------|
| Text / numbers     | As is                                 | Inline text input           |
| Date / time / timestamp | ISO 8601 (`2006-01-02 15:04:05[.000] ±07:00`) | Dialog with a calendar (dates) and an `HH:MM:SS` field (times), or **Set Now**; never typed text |
| BLOB / bytea       | `<BLOB 1.2 KB>` + first bytes in hex  | **Read-only**               |
| JSON / JSONB       | Text                                  | Inline text input           |
| NULL               | Dimmed `NULL`, distinct from `''`     | **Set NULL** (`Ctrl+0` in the input) if the column is nullable |

The grids (Structure, Data and SQL results) draw every border, inner and outer, and the header row has a
background different from the data rows. The Data grid starts with a `#` column that numbers the rows shown
from 1. Cells show only the first line of the value, cut at 30 characters
and ending in `…` if something was trimmed. Columns that do not fit in the panel width are reachable with
horizontal scroll (scrollbar or Shift+wheel).

### 6.5 Foreign key navigation

- When a table is opened its foreign keys are read along with its columns (not for views). The Structure tab
  shows them in the Key column (`FK → table.column`).
- In the Data tab, every non-NULL cell of a foreign key column has a link button after the value. It opens the
  referenced table in the Data tab, filtered by `refcol = value` (every column of a composite FK); the
  filter is shown in the toolbar and applies to pagination and `COUNT(*)`.
- A table opened this way has a back button in its header that returns to the previous table with its page
  as it was. Opening a table from the sidebar starts over.
- SQL editor results have no links (their columns are not tied to a table).

---

## 7. Grid data editing

### 7.1 Where editing is allowed

- Only in the **Data tab** of a table, opened from the sidebar or by following a foreign key (§6.5). SQL
  editor results are **never** editable.
- If the connection is marked `ReadOnly`, the grid is read-only.
- Views are read-only. BLOB columns are read-only.
- MongoDB (§2.3): documents are identified by `_id` and saved with `insertOne`, `updateOne` (`$set` of the
  edited fields) and `deleteOne`, shown as mongosh calls in the review. Updates and deletes must find exactly
  one document. On a replica set or `mongos` they run in one transaction; a standalone server has none, so
  the review says that changes are saved one by one and, on a failure, the error says how many were saved
  and the page is reloaded. Fields the sample did not find are not editable in the grid (use a command).

### 7.2 Pending changes

- Every edit, insertion or row deletion stays **pending** until it is saved. Modified cells are highlighted,
  new rows go on top of the grid marked `+` (unset columns show `DEFAULT`) and deleted rows are struck
  through (their `#` number too).
- Pending changes refer to the rows of the current page.
- **Save (N)** opens a preview of the SQL that will run (with the values inlined, only to show them) and, on
  confirmation, applies everything in **one transaction**, in the order the changes were made. If a
  statement fails, everything is rolled back, the error is shown in the preview naming the change
  (for example "The DELETE of row 1 failed: …") and the changes stay pending so they can be fixed.
- **Cancel** discards every pending change.
- Changing page, refreshing, changing the page size, going back, closing the tab, disconnecting or
  deleting the connection with pending changes asks first: **Save…**, **Discard** or **Cancel**. Opening
  another table or switching tabs does not: the changes stay in their tab (§4.1).
- Closing the window with unsaved changes (including a cell being edited) asks to **Keep editing** or
  **Discard and close**.

### 7.3 Row identification

- **With a PK**: `UPDATE/DELETE … WHERE pk1 = ? [AND pk2 = ?]` with the original values.
- **Without a PK**: the `WHERE` uses **every comparable column** with its original values (`col IS NULL`
  for NULLs; binary, JSON, XML, geometric and floating point columns are excluded). A warning is shown
  before the first change to an existing row.
- **Single-row guarantee**: after each `UPDATE`/`DELETE`, `RowsAffected == 1` is checked. If it is any
  other number, the **whole transaction is rolled back** and the offending row is reported
  (for example "would affect 3 identical rows", or "found no row" if it was changed or deleted meanwhile).

### 7.4 DML generation

- Always with parameters (`Placeholder`) and table and column names with `QuoteIdent`; values are never
  concatenated. Values are sent as text (NULL as `nil`) and the engine converts them to the column type.
- `INSERT` only lists the columns the user set, omitting identity/autoincrement columns left empty; with no
  columns it inserts the defaults.
- MySQL does not accept the offset the grid shows in dates (`… +00:00`): dates are sent in UTC without it.
  MySQL `BOOL` is `TINYINT` (0/1).
- SQL Server and Oracle get dates as `time.Time` (Oracle would parse text with `NLS_DATE_FORMAT`). An
  Oracle `DATE` also has a time of day: the grid shows it unless it is midnight.

### 7.5 Editing in the Data tab

- **Selection**: clicking a cell selects its row and opens the cell's editor. Ctrl+click (Cmd on macOS) adds
  or removes rows and Shift+click selects a range from the last row picked, without editing. Clicking a `#`
  number selects the row without editing. Selected rows have their number filled with the accent color and a
  tinted background; the clicked cell is underlined.
- **Editors**: text and numbers use an inline input: Enter or leaving the cell keeps the value, Esc
  discards it, `Ctrl+0` sets NULL. **Dates and times never ask for text**: a dialog with a calendar (dates)
  and an `HH:MM:SS` field (times) opens on the cell's value, with **Now**, **Set NULL** (if nullable),
  **Cancel** and **OK**. The offset is kept for `with time zone` types.
- **Bar below the grid**:

  | Group | Buttons | Acts on |
  |-------|---------|---------|
  | Changes | **Save (N)**, **Cancel** | Every pending change |
  | Rows | **Add**, **Delete (N)** / **Restore** | Add: a new row on top, editing its first column. Delete: the selected rows (new rows are removed); Restore when all of them are already deleted |
  | Values | **Set NULL**, **Set Now** | The column of the clicked cell in every selected row (or the cell being edited). Set NULL only for nullable columns; Set Now only for date/time columns |

  On the right, the bar shows how many rows are selected and a summary of the pending changes.

---

## 8. Safeguards

- **Read-only connection**: besides disabling grid editing, the SQL editor rejects statements that are not
  reads (`SELECT`, `WITH` without DML, `SHOW`, `EXPLAIN`, read `PRAGMA`s).
  In PG the session is also opened with `default_transaction_read_only = on`; in MySQL with
  `SET SESSION TRANSACTION READ ONLY`. SQL Server and Oracle have no read-only sessions: only anvil's checks
  apply (SQL Server connections declare `ApplicationIntent=ReadOnly`).
- **DELETE/UPDATE without WHERE**: when the SQL editor detects an `UPDATE` or `DELETE` without `WHERE`, it asks
  for explicit confirmation before running it.

---

## 9. Export

- From any grid: **CSV** (RFC 4180, UTF-8, with headers) or **JSON** (array of objects).
- Scope: the current page or the full result (re-run in streaming, without loading everything into
  memory).
- NULL: empty cell in CSV and `null` in JSON. BLOB as base64. Dates in ISO 8601 (RFC 3339). In JSON, numbers
  and booleans keep their type; everything else is a string.
- **Export** button in the Data tab toolbar and above the SQL results: a dialog picks the format and the rows,
  then the native save dialog the file. The full result of a table re-reads it with its foreign key filter, in
  PK order; the full result of a query runs it again without pagination (not offered for a non-repeatable
  result, such as `INSERT … RETURNING`). Rows are written as they are read (`query.StreamTable`,
  `query.StreamRows`, `export.Writer`).
- MongoDB: a full collection export has every top-level field of the collection (collected first with an
  aggregation), in `_id` order; a full command result runs the cursor command twice (fields, then documents).
- The file is written to a temporary file in the same directory and renamed at the end: a failed or cancelled
  export (Cancel in the dialog) leaves no half-written file and does not touch an existing one.

---

## 10. Theme

- Light and dark, switchable from the top bar and persisted in `Settings`.
- Icons are own SVGs with `stroke="currentColor"`; they are painted with the active theme's text color
  (`SvgCfg.Color`), so they must be re-rendered when the theme changes.

---

## 11. Errors

- Every error that reaches the UI is an `*appErrors.AppError` (Title + Description + wrapped Err).
- Add `Unwrap() error` to `AppError` so `errors.Is/As` can be used.
- UI messages stay in English (no i18n in v1).

---

## 12. Current state and detected debt

The 6 findings from the initial review are resolved (milestone 1):

1. ~~`string(conn.ID)` as the keyring key~~ → `strconv.FormatUint` (`connections.keyringKey`).
2. ~~Connection created even if `keyring.Set` fails~~ → `Save` runs in a transaction and, if the keyring fails,
   saves with `SavePassword=false` and returns `SaveResult.Warning`.
3. ~~`delete.go` does not delete the keyring entry or check rows~~ → looks up the row (not found), deletes it and cleans the keyring.
4. ~~Incomplete message in `password.go`~~ → complete messages and wrapped `err` (`ErrPasswordNotFound`).
5. ~~300×300 window with a red border~~ → 1200×760 window with the §4 layout.
6. ~~Empty `internal/interfaces`~~ → removed.

### 12.1 Progress

- **Milestone 1 (done)**: models, local database, connection service, drivers (SQLite/PG/MySQL with metadata,
  `SplitStatements`, `Analyze`), sessions, settings, UI with top bar, sidebar (connections + tables with
  filter), create/edit modal with Test connection, password prompt, delete with confirmation, persisted
  light/dark theme and structure view of the selected table.
- **Milestone 2 (done)**: Structure / Data / SQL tabs per table, read-only paginated grid (keyset or
  OFFSET, on-demand COUNT, dimmed NULL, summarized BLOB), SQL editor with go-edit: Ctrl+Enter (selection or
  statement under the cursor), several statements with the last result, warnings (no WHERE / no transaction),
  read-only, Stop, results pagination and saving to history (`internal/query`, `internal/history`).
- **Foreign key navigation** in the Data tab (§6.5): link buttons on FK cells, filtered referenced table,
  back button.
- **Milestone 3**: autocompletion (done), metadata refresh (done, sidebar refresh). Pending: history panel
  with search.
- **Milestone 4 (done)**: grid editing (`internal/editing`, §7): pending changes, SQL preview, single
  transaction with the `RowsAffected == 1` check, row identification with or without a PK, multi-row
  selection with the `#` column, bar below the grid (Save, Cancel | Add, Delete | Set NULL, Set Now),
  calendar/time editor for dates, and confirmation before leaving or closing the window with unsaved
  changes.
- **Tabs** (§4.1): table and SQL tabs from any connection, preview tab, pending changes per tab, close guard
  and shortcuts (`ui/tabs.go`). **Refresh** of the table and database lists from the sidebar.
- **Creating tables and databases** (§3.5): `internal/schema`, New table dialog with SQL preview, New
  database for server connections and New… SQLite file in the connection form.
- **Overflow fixes**: dialogs and the sidebar keep long values inside them (§4), checked by `TestNoOverflow`.
- **README** with screenshots of a demo database, regenerated by `TestReadmeScreenshots`
  (`ANVIL_README_SHOTS=docs/screenshots`).
- **MongoDB** (§2.3, §5.3): connection (host or connection string), databases and collections, document
  grid with editing by `_id` (transactional on replica sets), commands in the editor and export
  (`internal/mongodb`). **Documents view** (`ui/docView.go`): one card per document with expandable
  objects and arrays (read-only), the default in the Data tab, with a switch to the grid.
- **Milestone 5**: export, packaging and release. Done so far: `makefile` with per-platform builds and
  GoReleaser on GitHub Actions for `v*` tags (§13), and export to CSV/JSON (§9). Pending: the macOS build
  (§13) and the packages.

### 12.2 Known dependency limitations

- go-gui v0.92: a vertical `gui.Splitter` inside another `gui.Splitter` doubles the row height of
  `gui.Table`. The SQL tab splits editor/results in a fixed proportion (45 %) instead of using a splitter.
- go-gui v0.92: `datagrid.GridCellFormat` has no public constructor; the grids use `gui.Table`, which supports
  per-cell styling.
- go-gui v0.92: a horizontally scrolling container stores the content width before the table takes its
  final width and clips the scroll to that value. The grid measures its natural width every frame and passes
  it as `MinWidth` (`naturalWidth` in `ui/resultGrid.go`).
- go-gui v0.92: `gui.Table` has no header color; row 0 is painted as selected (`gridStyle`). It has no
  per-row background either: selected rows are shown with the `#` number filled and a text background.
- go-gui v0.92: `gui.Table` measures column widths from `TableCellCfg.Value` even when the cell has
  `Content` (buttons, inputs): the value is padded to make room. `TableCellCfg.ID` is not applied to the cell.
- go-gui v0.92: `gui.DatePicker` only picks days; times use a masked input next to it.
- go-gui v0.92: a single-line `gui.Text` cannot shrink below its measured width, so a long value widens
  every container up to the window. Labels that may be long sit in a `FillFit` row with `Clip`
  (`components.TreeLabel`), as go-gui's Combobox does. The dialog title is a single-line text too: user
  values in it are cut with `components.Ellipsis`. Wrapped text does not break words longer than the line.
- go-gui v0.92: headless UI tests must call `soft.RenderToImage` before checking sizes; without it texts get
  default widths and an overflow goes unnoticed.
- go-edit v0.12: the editor keeps the text measurer of the first software render; a second
  `soft.RenderToImage`/`RenderToPNG` of the same window uses it after it was closed and panics. Only affects
  headless screenshots of the SQL tab: they render once.
- go-edit v0.12: the editor lays out a floating layer that sticks out of its pane, so `TestNoOverflow` does
  not check a window with an SQL tab active.
- go-gui v0.92: there is no click count; double clicks (sidebar table, tab) are detected with the time between
  two clicks on the same widget (`App.doubleClick`, 400 ms).
- go-edit v0.12: no completion popup. Anvil draws its own (`ui/completion.go`): actions read the cursor, scroll
  and measurer through reflection (`ui/editorState.go`) and place the popup with go-edit's draw formula, which
  assumes no line wrap and no folding (the SQL editor uses neither). The text area offset (the editor's
  default padding) is measured from the layout.
- go-edit v0.12: `ActionFunc` takes an unexported type; the run action is declared generic
  (`runAction[S any]`) and reads the cursors through reflection.
- When an execution ends with an open transaction (`BEGIN` without `COMMIT`), a `ROLLBACK` is issued and the
  status bar says so: each execution uses its own session.

### 12.3 Review backlog (October 2026)

Findings from a project review at `6d19248`, to take into account in the next versions.

**Possible bugs**

1. **Pagination fallback inside a PG transaction** (`internal/query/exec.go`, `fetch`): if the wrapped
   `SELECT * FROM (…) anvil_q` fails after a `BEGIN` (for example duplicate columns in a `JOIN`), PG aborts the
   transaction and the unwrapped retry fails with "current transaction is aborted". Use a `SAVEPOINT`
   before the wrapped attempt, or do not wrap while a transaction is open.
2. **`returning()` with `strings.Contains`** (`internal/query/exec.go`): a `'returning'` literal or a
   `returning_date` column is taken as `… RETURNING`. Detect it with the tokens of `drivers/lexer.go`.
3. **Implicit transactions in Oracle and SQL Server** (`txOpen`, `internal/query/plan.go`): only
   `BEGIN`/`START` open a transaction. In Oracle any DML opens one without `BEGIN` (and so does SQL Server with
   `SET IMPLICIT_TRANSACTIONS ON`). Check what `go-ora` does when that connection goes back to the pool with
   uncommitted DML: a later use could commit it.
4. **macOS release binaries** (`.goreleaser.yaml`): darwin is built with `CGO_ENABLED=0`, which panics on
   startup (§13). Leave darwin out of the release until there is a job on a macOS runner with CGO.

**To add**

- CI on every PR (`go vet`, `go test`), not only on `v*` tags; optionally with PostgreSQL and MySQL services
  and the `ANVIL_TEST_*` variables.
- ~~Export to CSV/JSON (§9) before 1.0.~~ Done.
- SSH tunnel: move it up, ahead of polishing Oracle and SQL Server.
- Configurable query timeout, and a warning when the client-side pagination fallback has to read and
  discard many rows to reach the offset.
- Use the connection `Color` to tint the top bar or the SQL tab, as a signal for production connections.

**To remove or simplify**

- Mark Oracle and SQL Server as *experimental* in the README until CI covers them against real servers; focus
  1.0 on SQLite, PostgreSQL and MySQL.
- Translate the remaining Spanish comments (`main.go`, `.goreleaser.yaml`) to English.
- Reduce the duplication between §12 and the README feature list: progress lives here, the README stays
  user-facing.

---

## 13. Distribution and license

- **License**: MIT (`LICENSE` file at the root).
- **Binaries** on GitHub Releases for Linux (amd64/arm64), Windows (amd64/arm64) and macOS (amd64/arm64),
  built by GoReleaser on GitHub Actions when a `v*` tag is pushed (`.goreleaser.yaml`,
  `.github/workflows/release.yml`): tests, `.tar.gz` (`.zip` on Windows) archives with `LICENSE` and
  `README.md`, and `SHA256SUMS`. `-X main.version` stamps the version (`anvil --version`); Windows builds
  use `-H=windowsgui` so no console opens. Tags with a suffix (`v0.5.0-beta`) are pre-releases.
- Linux and Windows build without CGO thanks to the pure Go SQLite (§2.1). **macOS needs CGO**: go-gui's
  Metal backend is Objective-C. A `CGO_ENABLED=0` darwin build compiles but has no backend and panics on
  startup, so macOS binaries must be built on a macOS runner with CGO (pending: the current GoReleaser
  config and `makefile` build darwin with `CGO_ENABLED=0`).
- The `makefile` builds locally: `make build`, `make build-<os>-<arch>`, `make release` (every platform
  plus `SHA256SUMS` in `dist/`).
- **Packages**: AUR (Linux), Homebrew (macOS) and winget (Windows), published from the same release
  pipeline.
