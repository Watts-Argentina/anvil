// Package query runs SQL against the user's databases: table pages,
// editor statements and value conversion for the grid (SPEC §5, §6).
package query

import (
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Value is a cell. Text is what is shown; Raw is the scanned value, to be
// used as a parameter (keyset, DML).
type Value struct {
	Null bool
	Blob bool // binary: Text is a summary and the cell is read-only
	Text string
	Raw  any
}

type Column struct {
	Name   string
	DBType string
}

// Result is a set of already converted rows.
type Result struct {
	Columns []Column
	Rows    [][]Value
}

const (
	timeLayout = "2006-01-02 15:04:05.999 -07:00"
	dateLayout = "2006-01-02"
	blobPeek   = 8 // bytes shown in hex
)

// scan reads up to limit rows from rows after skipping skip. more reports
// that there was at least one more row.
func scan(rows *sql.Rows, skip, limit int) (res Result, more bool, err error) {
	n := 0
	err = each(rows, func(cols []Column) error {
		res.Columns = cols
		return nil
	}, func(row []Value) error {
		n++
		switch {
		case n <= skip:
		case len(res.Rows) == limit:
			more = true
			return errStop
		default:
			res.Rows = append(res.Rows, row)
		}
		return nil
	})
	if err != nil {
		return Result{}, false, err
	}
	return res, more, nil
}

// errStop ends each early without an error.
var errStop = errors.New("stop")

// each calls header with the columns and then row for every converted
// row, until one of them returns an error. It closes rows.
func each(rows *sql.Rows, header func([]Column) error, row func([]Value) error) error {
	defer rows.Close()

	types, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	cols := make([]Column, len(types))
	for i, t := range types {
		cols[i] = Column{Name: t.Name(), DBType: strings.ToUpper(t.DatabaseTypeName())}
	}
	if err := header(cols); err != nil {
		return err
	}

	dest := make([]any, len(types))
	ptrs := make([]any, len(types))
	for i := range dest {
		ptrs[i] = &dest[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		out := make([]Value, len(dest))
		for i, v := range dest {
			out[i] = convert(v, cols[i].DBType)
		}
		if err := row(out); err != nil {
			if errors.Is(err, errStop) {
				return nil
			}
			return err
		}
	}
	return rows.Err()
}

// convert normalizes a scanned value for display (SPEC §6.4).
func convert(v any, dbType string) Value {
	switch x := v.(type) {
	case nil:
		return Value{Null: true}
	case []byte:
		b := append([]byte(nil), x...)
		if dbType == "UNIQUEIDENTIFIER" && len(b) == 16 {
			return Value{Text: guid(b), Raw: b}
		}
		if IsBinaryType(dbType) || !utf8.Valid(b) {
			return Value{Blob: true, Text: blobSummary(b), Raw: b}
		}
		return Value{Text: string(b), Raw: b}
	case string:
		return Value{Text: x, Raw: x}
	case time.Time:
		// An Oracle DATE also has a time of day: only midnight is shown as
		// a date.
		if dbType == "DATE" && x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
			return Value{Text: x.Format(dateLayout), Raw: x}
		}
		return Value{Text: x.Format(timeLayout), Raw: x}
	case int64:
		return Value{Text: strconv.FormatInt(x, 10), Raw: x}
	case float64:
		return Value{Text: strconv.FormatFloat(x, 'g', -1, 64), Raw: x}
	case float32:
		return Value{Text: strconv.FormatFloat(float64(x), 'g', -1, 32), Raw: x}
	case bool:
		return Value{Text: strconv.FormatBool(x), Raw: x}
	case driver.Valuer:
		// Driver-specific types (pgtype.Numeric, …).
		inner, err := x.Value()
		if err == nil {
			if _, again := inner.(driver.Valuer); !again {
				val := convert(inner, dbType)
				val.Raw = v
				return val
			}
		}
	}
	return Value{Text: fmt.Sprint(v), Raw: v}
}

// IsBinaryType reports whether an (upper-case) engine type is binary.
func IsBinaryType(dbType string) bool {
	return strings.Contains(dbType, "BLOB") || strings.Contains(dbType, "BYTEA") ||
		strings.Contains(dbType, "BINARY") || dbType == "IMAGE" || strings.HasSuffix(dbType, "RAW")
}

// guid formats a SQL Server uniqueidentifier, whose first three groups are
// little-endian.
func guid(b []byte) string {
	return fmt.Sprintf("%X-%X-%X-%X-%X",
		[]byte{b[3], b[2], b[1], b[0]}, []byte{b[5], b[4]}, []byte{b[7], b[6]}, b[8:10], b[10:])
}

// blobSummary builds "<BLOB 1.2 KB> 0a1b2c…".
func blobSummary(b []byte) string {
	peek := b
	if len(peek) > blobPeek {
		peek = peek[:blobPeek]
	}
	s := "<BLOB " + humanSize(len(b)) + ">"
	if len(peek) > 0 {
		s += " " + hex.EncodeToString(peek)
		if len(b) > blobPeek {
			s += "…"
		}
	}
	return s
}

func humanSize(n int) string {
	switch {
	case n < 1024:
		return strconv.Itoa(n) + " B"
	case n < 1024*1024:
		return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + " KB"
	default:
		return strconv.FormatFloat(float64(n)/(1024*1024), 'f', 1, 64) + " MB"
	}
}

// Values returns the texts of a row (useful in tests and export).
func Values(row []Value) []string {
	out := make([]string, len(row))
	for i, v := range row {
		out[i] = v.Text
	}
	return out
}

// Text formats a raw value the way the grid shows it.
func Text(v any) string { return convert(v, "").Text }
