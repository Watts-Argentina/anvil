package completion

import (
	"slices"

	"github.com/Watts-Argentina/anvil/models"
)

// common are the keywords every engine shares, most used first.
var common = []string{
	"SELECT", "FROM", "WHERE", "AND", "OR", "NOT", "NULL", "IS", "IN", "LIKE",
	"BETWEEN", "EXISTS", "AS", "DISTINCT", "JOIN", "INNER", "LEFT", "RIGHT",
	"FULL", "OUTER", "CROSS", "ON", "USING", "GROUP", "BY", "HAVING", "ORDER",
	"ASC", "DESC", "UNION", "ALL", "INTERSECT", "EXCEPT", "CASE", "WHEN",
	"THEN", "ELSE", "END", "INSERT", "INTO", "VALUES", "UPDATE", "SET",
	"DELETE", "CREATE", "TABLE", "VIEW", "INDEX", "UNIQUE", "PRIMARY", "KEY",
	"FOREIGN", "REFERENCES", "DEFAULT", "CHECK", "CONSTRAINT", "ALTER", "ADD",
	"DROP", "COLUMN", "DATABASE", "BEGIN", "COMMIT", "ROLLBACK", "TRANSACTION",
	"WITH", "RECURSIVE", "OVER", "PARTITION", "COUNT", "SUM", "AVG", "MIN",
	"MAX", "COALESCE", "NULLIF", "CAST", "TRUE", "FALSE", "CURRENT_DATE",
	"CURRENT_TIMESTAMP",
}

var extra = map[string][]string{
	models.EngineSQLite:   {"LIMIT", "OFFSET", "AUTOINCREMENT", "PRAGMA", "RETURNING", "REPLACE", "VACUUM", "EXPLAIN"},
	models.EnginePostgres: {"LIMIT", "OFFSET", "RETURNING", "ILIKE", "SERIAL", "SCHEMA", "EXPLAIN", "ANALYZE", "CONFLICT", "DO", "NOTHING", "NOW"},
	models.EngineMySQL:    {"LIMIT", "OFFSET", "AUTO_INCREMENT", "SHOW", "DESCRIBE", "EXPLAIN", "REPLACE", "DUPLICATE", "IFNULL", "NOW"},
	models.EngineMSSQL:    {"TOP", "OFFSET", "FETCH", "NEXT", "ROWS", "ONLY", "IDENTITY", "GO", "TRAN", "ISNULL", "GETDATE", "NOLOCK"},
	models.EngineOracle:   {"FETCH", "FIRST", "NEXT", "ROWS", "ONLY", "ROWNUM", "DUAL", "SYSDATE", "NVL", "SEQUENCE", "MERGE"},
}

// Keywords returns the keywords of engine.
func Keywords(engine string) []string {
	return append(slices.Clip(common), extra[engine]...)
}
