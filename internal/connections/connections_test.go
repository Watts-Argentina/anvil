package connections

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Watts-Argentina/anvil/internal/storage"
	"github.com/Watts-Argentina/anvil/models"
	"github.com/Watts-Argentina/anvil/pkg/appErrors"
	"github.com/zalando/go-keyring"
)

func newService(t *testing.T) *Service {
	t.Helper()
	db, err := storage.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	return &Service{DB: db}
}

func pgConn(name string) models.DBConn {
	return models.DBConn{
		Name: name, Engine: models.EnginePostgres, Host: "localhost",
		Port: 5432, User: "postgres", DB: "app", SavePassword: true,
	}
}

func TestSaveStoresPasswordWithNumericKey(t *testing.T) {
	keyring.MockInit()
	s := newService(t)
	ctx := context.Background()

	// Create several so the ID goes past the low ASCII values.
	var res SaveResult
	for i := range 3 {
		var err error
		res, err = s.Save(ctx, pgConn(fmt.Sprint("c", i)), "secret")
		if err != nil {
			t.Fatal(err)
		}
	}
	if res.Warning != nil {
		t.Fatalf("unexpected warning: %v", res.Warning)
	}

	if _, err := keyring.Get(service, "3"); err != nil {
		t.Fatalf("password not stored under key \"3\": %v", err)
	}
	p, err := s.GetPassword(ctx, res.Conn.ID)
	if err != nil || p != "secret" {
		t.Fatalf("GetPassword = %q, %v", p, err)
	}
}

func TestSaveFallsBackWhenKeyringFails(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))
	s := newService(t)
	ctx := context.Background()

	res, err := s.Save(ctx, pgConn("pg"), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if res.Warning == nil {
		t.Fatal("expected warning")
	}
	stored, err := s.Get(ctx, res.Conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SavePassword || res.Conn.SavePassword {
		t.Fatal("SavePassword should be false after keyring failure")
	}
}

func TestSaveUpdateKeepsPasswordWhenEmpty(t *testing.T) {
	keyring.MockInit()
	s := newService(t)
	ctx := context.Background()

	res, err := s.Save(ctx, pgConn("pg"), "secret")
	if err != nil {
		t.Fatal(err)
	}
	conn := res.Conn
	conn.Name = "renamed"
	if _, err := s.Save(ctx, conn, ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.GetPassword(ctx, conn.ID); p != "secret" {
		t.Fatalf("password changed to %q", p)
	}
	if got, _ := s.Get(ctx, conn.ID); got.Name != "renamed" {
		t.Fatalf("name = %q", got.Name)
	}

	conn.SavePassword = false
	if _, err := s.Save(ctx, conn, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPassword(ctx, conn.ID); !errors.Is(err, ErrPasswordNotFound) {
		t.Fatalf("expected ErrPasswordNotFound, got %v", err)
	}
}

func TestDeleteRemovesKeyringEntry(t *testing.T) {
	keyring.MockInit()
	s := newService(t)
	ctx := context.Background()

	res, err := s.Save(ctx, pgConn("pg"), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, res.Conn.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(service, keyringKey(res.Conn.ID)); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("keyring entry still present: %v", err)
	}

	err = s.Delete(ctx, res.Conn.ID)
	var ae *appErrors.AppError
	if !errors.As(err, &ae) || ae.Title != "Connection not found." {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		conn models.DBConn
		ok   bool
	}{
		{"pg ok", pgConn("pg"), true},
		{"no name", models.DBConn{Engine: models.EngineSQLite, DB: "a.db"}, false},
		{"sqlite ok", models.DBConn{Name: "s", Engine: models.EngineSQLite, DB: "a.db"}, true},
		{"sqlite no file", models.DBConn{Name: "s", Engine: models.EngineSQLite}, false},
		{"bad engine", models.DBConn{Name: "s", Engine: "oracle"}, false},
		{"bad port", func() models.DBConn { c := pgConn("pg"); c.Port = 0; return c }(), false},
		{"bad color", func() models.DBConn { c := pgConn("pg"); c.Color = "red"; return c }(), false},
		{"mongo no user", models.DBConn{Name: "m", Engine: models.EngineMongo, Host: "localhost", Port: 27017}, true},
		{"mongo uri", models.DBConn{Name: "m", Engine: models.EngineMongo, Host: "mongodb+srv://c.example.net"}, true},
		{"mongo no host", models.DBConn{Name: "m", Engine: models.EngineMongo, Port: 27017}, false},
		{"mongo bad port", models.DBConn{Name: "m", Engine: models.EngineMongo, Host: "localhost"}, false},
	}
	for _, tc := range cases {
		if err := Validate(tc.conn); (err == nil) != tc.ok {
			t.Errorf("%s: Validate = %v", tc.name, err)
		}
	}
}

func TestListOrdersByName(t *testing.T) {
	keyring.MockInit()
	s := newService(t)
	ctx := context.Background()
	for _, n := range []string{"zeta", "Alpha", "beta"} {
		if _, err := s.Save(ctx, pgConn(n), ""); err != nil {
			t.Fatal(err)
		}
	}
	conns, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range conns {
		names = append(names, c.Name)
	}
	if fmt.Sprint(names) != "[Alpha beta zeta]" {
		t.Fatalf("order = %v", names)
	}
}
