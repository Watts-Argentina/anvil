// Package session keeps the open connections to the user's databases:
// one *sql.DB per connection (SPEC §3.3).
package session

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/Watts-Argentina/anvil/internal/drivers"
	"github.com/Watts-Argentina/anvil/internal/mongodb"
	"github.com/Watts-Argentina/anvil/models"
	"github.com/Watts-Argentina/anvil/pkg/appErrors"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Session is an open connection. Conn is a copy of the configuration at
// connect time.
//
// If the connection has no database configured (drivers.AllDatabases), DB is
// the maintenance database pool and each server database is opened in its
// own pool when requested with Pool.
//
// A MongoDB session has Mongo instead of Driver and DB (SPEC §2.3): one
// client serves every database.
type Session struct {
	Conn   models.DBConn
	Driver drivers.Driver
	DB     *sql.DB

	Mongo *mongo.Client
	// Transactions reports that the MongoDB server supports them (replica
	// set or mongos): saving grid changes is all or nothing.
	Transactions bool

	// The password stays in memory only while the session is open
	// (SPEC §3.2), to open the pools of the other databases.
	password string

	mu    sync.Mutex
	pools map[string]*sql.DB
}

// AllDatabases reports whether the session shows every server database.
func (s *Session) AllDatabases() bool {
	return drivers.AllDatabases(s.Conn)
}

// IsMongo reports whether the session is a MongoDB one.
func (s *Session) IsMongo() bool { return s.Mongo != nil }

// MongoDB returns a MongoDB database ("" = the configured one).
func (s *Session) MongoDB(database string) *mongo.Database {
	if database == "" {
		database = s.Conn.DB
	}
	return s.Mongo.Database(database)
}

// Collection returns a collection of a MongoDB database.
func (s *Session) Collection(database string, t drivers.Table) *mongo.Collection {
	return s.MongoDB(database).Collection(t.Name)
}

// Databases lists the server databases.
func (s *Session) Databases(ctx context.Context) ([]string, error) {
	var dbs []string
	var err error
	if s.IsMongo() {
		dbs, err = mongodb.ListDatabases(ctx, s.Mongo)
	} else {
		dbs, err = s.Driver.ListDatabases(ctx, s.DB)
	}
	if err != nil {
		return nil, appErrors.Error(
			"Failed to list databases.",
			"The databases of the server could not be read: "+err.Error(),
			err,
		)
	}
	return dbs, nil
}

// Pool returns the pool for database. With "" (or if the connection has a
// database configured) it returns the main pool.
func (s *Session) Pool(ctx context.Context, database string) (*sql.DB, error) {
	if s.IsMongo() {
		return nil, appErrors.Error("Not an SQL connection.", "MongoDB connections have no SQL pool.", errNoPool)
	}
	if database == "" || !s.AllDatabases() || database == s.Driver.MaintenanceDB() {
		return s.DB, nil
	}

	s.mu.Lock()
	db, ok := s.pools[database]
	s.mu.Unlock()
	if ok {
		return db, nil
	}

	c := s.Conn
	c.DB = database
	db, err := drivers.Connect(ctx, c, s.password)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pools == nil {
		// Close already ran.
		db.Close()
		return nil, errSessionClosed
	}
	if existing, ok := s.pools[database]; ok {
		db.Close()
		return existing, nil
	}
	s.pools[database] = db
	return db, nil
}

var errNoPool = errors.New("mongodb session has no sql pool")

var errSessionClosed = appErrors.Error("Connection closed.", "The connection was closed.", sql.ErrConnDone)

// Tables lists the tables and views of database ("" = the main database).
func (s *Session) Tables(ctx context.Context, database string) ([]drivers.Table, error) {
	if s.IsMongo() {
		tables, err := mongodb.ListCollections(ctx, s.MongoDB(database))
		if err != nil {
			return nil, appErrors.Error("Failed to list collections.", "The collections of the database could not be read: "+err.Error(), err)
		}
		return tables, nil
	}
	db, err := s.Pool(ctx, database)
	if err != nil {
		return nil, err
	}
	tables, err := s.Driver.ListTables(ctx, db)
	if err != nil {
		return nil, appErrors.Error(
			"Failed to list tables.",
			"The tables of the database could not be read: "+err.Error(),
			err,
		)
	}
	return tables, nil
}

// Columns returns the columns of a table or view of database.
func (s *Session) Columns(ctx context.Context, database string, t drivers.Table) ([]drivers.Column, error) {
	if s.IsMongo() {
		cols, err := mongodb.Columns(ctx, s.Collection(database, t))
		if err != nil {
			return nil, appErrors.Error("Failed to read fields.", "The fields of "+t.Name+" could not be read: "+err.Error(), err)
		}
		return cols, nil
	}
	db, err := s.Pool(ctx, database)
	if err != nil {
		return nil, err
	}
	cols, err := s.Driver.Columns(ctx, db, t)
	if err != nil {
		return nil, appErrors.Error(
			"Failed to read columns.",
			"The columns of "+t.Name+" could not be read: "+err.Error(),
			err,
		)
	}
	return cols, nil
}

// ForeignKeys returns the foreign keys of a table of database.
func (s *Session) ForeignKeys(ctx context.Context, database string, t drivers.Table) ([]drivers.ForeignKey, error) {
	if s.IsMongo() {
		return nil, nil
	}
	db, err := s.Pool(ctx, database)
	if err != nil {
		return nil, err
	}
	fks, err := s.Driver.ForeignKeys(ctx, db, t)
	if err != nil {
		return nil, appErrors.Error(
			"Failed to read foreign keys.",
			"The foreign keys of "+t.Name+" could not be read: "+err.Error(),
			err,
		)
	}
	return fks, nil
}

// Close closes the main pool and the pool of each database.
func (s *Session) Close() error {
	s.mu.Lock()
	pools := s.pools
	s.pools = nil
	s.mu.Unlock()

	for _, db := range pools {
		db.Close()
	}
	if s.IsMongo() {
		return s.Mongo.Disconnect(context.Background())
	}
	return s.DB.Close()
}

// Manager is safe for concurrent use: it is called from worker goroutines,
// never from the UI thread with I/O.
type Manager struct {
	mu       sync.Mutex
	sessions map[uint]*Session
}

func NewManager() *Manager {
	return &Manager{sessions: map[uint]*Session{}}
}

// Connect opens the connection (Open + Ping). If it was already open, it
// returns the existing session.
func (m *Manager) Connect(ctx context.Context, conn models.DBConn, password string) (*Session, error) {
	if s, ok := m.Get(conn.ID); ok {
		return s, nil
	}
	if conn.Engine == models.EngineMongo {
		client, err := mongodb.Connect(ctx, conn, password)
		if err != nil {
			return nil, err
		}
		s := &Session{Conn: conn, Mongo: client, Transactions: mongodb.Transactions(ctx, client)}
		return m.add(s, func() { client.Disconnect(context.Background()) }), nil
	}

	d, err := drivers.For(conn.Engine)
	if err != nil {
		return nil, err
	}
	db, err := drivers.Connect(ctx, conn, password)
	if err != nil {
		return nil, err
	}

	s := &Session{Conn: conn, Driver: d, DB: db, password: password, pools: map[string]*sql.DB{}}
	return m.add(s, func() { db.Close() }), nil
}

// add registers s, unless another goroutine connected in the meantime:
// then it closes s with drop and returns the existing session.
func (m *Manager) add(s *Session, drop func()) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.sessions[s.Conn.ID]; ok {
		drop()
		return existing
	}
	m.sessions[s.Conn.ID] = s
	return s
}

func (m *Manager) Get(connID uint) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[connID]
	return s, ok
}

// Disconnect closes the connection if it was open.
func (m *Manager) Disconnect(connID uint) error {
	m.mu.Lock()
	s, ok := m.sessions[connID]
	delete(m.sessions, connID)
	m.mu.Unlock()

	if !ok {
		return nil
	}
	return s.Close()
}

// CloseAll closes every connection (when the app exits).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	sessions := m.sessions
	m.sessions = map[uint]*Session{}
	m.mu.Unlock()

	for _, s := range sessions {
		s.Close()
	}
}
