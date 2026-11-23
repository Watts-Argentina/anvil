// Package export writes results to CSV or JSON files (SPEC §9).
package export

import (
	"bufio"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/pkg/appErrors"
)

type Format string

const (
	CSV  Format = "csv"
	JSON Format = "json"
)

// Writer writes a result row by row; it is a query.RowSink. Close ends
// the document.
type Writer struct {
	format Format
	buf    *bufio.Writer
	csv    *csv.Writer
	keys   [][]byte // JSON-encoded column names
	rows   int
}

func NewWriter(w io.Writer, f Format) *Writer {
	b := bufio.NewWriter(w)
	return &Writer{format: f, buf: b, csv: csv.NewWriter(b)}
}

// Rows is the number of rows written.
func (w *Writer) Rows() int { return w.rows }

func (w *Writer) Header(cols []query.Column) error {
	if w.format == CSV {
		names := make([]string, len(cols))
		for i, c := range cols {
			names[i] = c.Name
		}
		return w.csv.Write(names)
	}
	// shortcut: repeated column names (a JOIN without aliases) repeat the
	// JSON key and readers keep only one; alias them in the query.
	w.keys = make([][]byte, len(cols))
	for i, c := range cols {
		w.keys[i], _ = json.Marshal(c.Name)
	}
	_, err := w.buf.WriteString("[")
	return err
}

func (w *Writer) Row(row []query.Value) error {
	w.rows++
	if w.format == CSV {
		rec := make([]string, len(row))
		for i, v := range row {
			rec[i] = text(v)
		}
		return w.csv.Write(rec)
	}
	if w.rows > 1 {
		w.buf.WriteString(",")
	}
	w.buf.WriteString("\n{")
	for i, v := range row {
		if i > 0 {
			w.buf.WriteString(",")
		}
		w.buf.Write(w.keys[i])
		w.buf.WriteString(":")
		w.buf.Write(jsonValue(v))
	}
	_, err := w.buf.WriteString("}")
	return err
}

func (w *Writer) Close() error {
	if w.format == CSV {
		w.csv.Flush()
		if err := w.csv.Error(); err != nil {
			return err
		}
	} else {
		if w.keys == nil { // no Header: an empty result
			w.buf.WriteString("[")
		}
		w.buf.WriteString("\n]\n")
	}
	return w.buf.Flush()
}

// text is a value in CSV: NULL is an empty cell, BLOBs are base64 and
// dates ISO 8601.
func text(v query.Value) string {
	if v.Null {
		return ""
	}
	switch x := v.Raw.(type) {
	case []byte:
		if v.Blob {
			return base64.StdEncoding.EncodeToString(x)
		}
	case time.Time:
		return x.Format(time.RFC3339Nano)
	}
	return v.Text
}

func jsonValue(v query.Value) []byte {
	if v.Null {
		return []byte("null")
	}
	var out any = text(v)
	switch x := v.Raw.(type) {
	case int64, float64, float32, bool:
		out = x
	}
	b, err := json.Marshal(out)
	if err != nil { // NaN or Inf
		b, _ = json.Marshal(v.Text)
	}
	return b
}

// ToFile writes path in format f with fill, through a temporary file in the
// same directory: an error leaves no half-written file behind. It returns
// the number of rows written.
func ToFile(path string, f Format, fill func(*Writer) error) (int, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".anvil-export-*")
	if err != nil {
		return 0, appErrors.Error("Export failed.", "Could not create the file: "+err.Error(), err)
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	tmp.Chmod(0o644)            // CreateTemp uses 0600; a saved file is readable like any other

	w := NewWriter(tmp, f)
	err = fill(w)
	if err == nil {
		err = w.Close()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		var ae *appErrors.AppError
		if errors.As(err, &ae) {
			return 0, err
		}
		return 0, appErrors.Error("Export failed.", "Could not write the file: "+err.Error(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return 0, appErrors.Error("Export failed.", "Could not save the file: "+err.Error(), err)
	}
	return w.Rows(), nil
}

// Result writes an already loaded result (the current page).
func Result(res query.Result) func(*Writer) error {
	return func(w *Writer) error {
		if err := w.Header(res.Columns); err != nil {
			return err
		}
		for _, row := range res.Rows {
			if err := w.Row(row); err != nil {
				return err
			}
		}
		return nil
	}
}
