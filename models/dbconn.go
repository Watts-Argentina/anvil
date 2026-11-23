package models

import (
	"gorm.io/gorm"
)

const (
	EngineSQLite   = "sqlite"
	EnginePostgres = "postgres"
	EngineMySQL    = "mysql"
	EngineMSSQL    = "mssql"
	EngineOracle   = "oracle"
	EngineMongo    = "mongodb"
)

type DBConn struct {
	gorm.Model
	Name         string `json:"name"`
	Color        string `json:"color"`
	Engine       string `json:"engine"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	DB           string `json:"database"` // for SQLite, the path to the file; for Oracle, the service name
	ReadOnly     bool   `json:"readOnly"`
	SSLMode      string `json:"sslMode"`
	SSLCA        string `json:"sslCA"`
	SSLCert      string `json:"sslCert"`
	SSLKey       string `json:"sslKey"`
	SavePassword bool   `json:"savePassword"`
}

// DefaultPort returns the engine's default port (0 for SQLite).
func DefaultPort(engine string) int {
	switch engine {
	case EnginePostgres:
		return 5432
	case EngineMySQL:
		return 3306
	case EngineMSSQL:
		return 1433
	case EngineOracle:
		return 1521
	case EngineMongo:
		return 27017
	}
	return 0
}
