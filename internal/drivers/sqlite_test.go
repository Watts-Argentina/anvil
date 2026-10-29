package drivers

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Watts-Argentina/anvil/models"
)

func newSQLiteFile(t *testing.T, ddl string) models.DBConn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test db#.sqlite")
	dsn, err := sqliteDSN(path, "rwc")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range (SQLite{}).SplitStatements(ddl) {
		if _, err := db.Exec(s.Text); err != nil {
			t.Fatalf("%s: %v", s.Text, err)
		}
	}
	return models.DBConn{Name: "test", Engine: models.EngineSQLite, DB: path}
}

const sqliteDDL = `
create table users (id integer primary key, name text not null, email text);
create table tags (user_id integer, tag text, primary key (user_id, tag));
create table logs (msg text);
create table kv (k integer primary key, v text) without rowid;
create view active as select * from users;
`

func TestSQLiteMetadata(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteFile(t, sqliteDDL)

	db, err := Connect(ctx, conn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	d := SQLite{}
	tables, err := d.ListTables(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	want := []Table{{Name: "active", IsView: true}, {Name: "kv"}, {Name: "logs"}, {Name: "tags"}, {Name: "users"}}
	if !reflect.DeepEqual(tables, want) {
		t.Fatalf("tables = %+v", tables)
	}

	cols, err := d.Columns(ctx, db, Table{Name: "users"})
	if err != nil {
		t.Fatal(err)
	}
	wantCols := []Column{
		{Name: "id", DBType: "INTEGER", PK: true, PKOrdinal: 1, AutoInc: true},
		{Name: "name", DBType: "TEXT"},
		{Name: "email", DBType: "TEXT", Nullable: true},
	}
	if !reflect.DeepEqual(cols, wantCols) {
		t.Fatalf("users columns = %+v", cols)
	}

	cols, _ = d.Columns(ctx, db, Table{Name: "tags"})
	if !cols[0].PK || cols[0].PKOrdinal != 1 || cols[1].PKOrdinal != 2 || cols[0].AutoInc {
		t.Fatalf("tags columns = %+v", cols)
	}
	cols, _ = d.Columns(ctx, db, Table{Name: "kv"})
	if cols[0].AutoInc {
		t.Fatal("WITHOUT ROWID integer pk is not autoincrement")
	}
}

func TestSQLiteForeignKeys(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteFile(t, sqliteDDL+`
		create table posts (id integer primary key, author integer references users, tagged_user integer, tag text,
			foreign key (tagged_user, tag) references tags (user_id, tag));`)
	db, err := Connect(ctx, conn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	fks, err := SQLite{}.ForeignKeys(ctx, db, Table{Name: "posts"})
	if err != nil {
		t.Fatal(err)
	}
	// pragma_foreign_key_list numbers the constraints from the last one.
	want := []ForeignKey{
		{Name: "0", Columns: []string{"tagged_user", "tag"}, RefTable: Table{Name: "tags"}, RefColumns: []string{"user_id", "tag"}},
		{Name: "1", Columns: []string{"author"}, RefTable: Table{Name: "users"}, RefColumns: []string{"id"}},
	}
	if !reflect.DeepEqual(fks, want) {
		t.Fatalf("foreign keys = %+v", fks)
	}
	if fks, err := (SQLite{}).ForeignKeys(ctx, db, Table{Name: "users"}); err != nil || len(fks) != 0 {
		t.Fatalf("users foreign keys = %+v, %v", fks, err)
	}
}

func TestSQLiteReadOnly(t *testing.T) {
	ctx := context.Background()
	conn := newSQLiteFile(t, sqliteDDL)
	conn.ReadOnly = true

	db, err := Connect(ctx, conn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec("insert into logs values ('x')"); err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Fatalf("expected readonly error, got %v", err)
	}
}

func TestSQLiteMissingFile(t *testing.T) {
	conn := models.DBConn{Engine: models.EngineSQLite, DB: filepath.Join(t.TempDir(), "nope.db")}
	if _, err := Connect(context.Background(), conn, ""); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestPostgresConfig(t *testing.T) {
	conn := models.DBConn{
		Engine: models.EnginePostgres, Host: "db.local", Port: 6543, User: "o'neil",
		DB: "my db", SSLMode: "disable", ReadOnly: true,
	}
	cfg, err := pgConfig(conn, `p'a ss\word`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "db.local" || cfg.Port != 6543 || cfg.User != "o'neil" || cfg.Database != "my db" {
		t.Fatalf("cfg = %+v", cfg.Config)
	}
	if cfg.Password != `p'a ss\word` || cfg.TLSConfig != nil {
		t.Fatal("password or TLS mismatch")
	}
	if cfg.RuntimeParams["default_transaction_read_only"] != "on" {
		t.Fatal("read-only param not set")
	}
}

func TestMySQLConfig(t *testing.T) {
	conn := models.DBConn{Engine: models.EngineMySQL, Host: "::1", User: "root", DB: "app"}
	cfg, err := mysqlConfig(conn, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "[::1]:3306" || cfg.Passwd != "secret" || !cfg.ParseTime || cfg.MultiStatements || !cfg.ClientFoundRows {
		t.Fatalf("cfg = %+v", cfg)
	}
	if _, err := mysqlConfig(models.DBConn{SSLMode: "bogus"}, ""); err == nil {
		t.Fatal("expected error for unknown SSL mode")
	}
}
