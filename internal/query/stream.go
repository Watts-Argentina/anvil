package query

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Watts-Argentina/anvil/internal/drivers"
	"github.com/Watts-Argentina/anvil/pkg/appErrors"
)

// RowSink receives a full result row by row, without keeping it in memory
// (export, SPEC §9).
type RowSink interface {
	Header(cols []Column) error
	Row(row []Value) error
}

// StreamTable reads every row of the table (with its filter) in PK order.
func StreamTable(ctx context.Context, src TableSource, sink RowSink) error {
	where, args := src.where("")
	q := "SELECT * FROM " + drivers.QualifiedName(src.Driver, src.Table) + where + src.orderBy()
	return stream(ctx, src.DB, q, args, sink)
}

// StreamRows runs the result's statement again, without pagination, and
// reads every row. Only for Repeatable pages.
func StreamRows(ctx context.Context, db *sql.DB, page RowsPage, sink RowSink) error {
	if !page.Repeatable {
		return appErrors.Error("Cannot export the full result.", "This result comes from a statement that modifies data; export the current page instead.", fmt.Errorf("non repeatable result"))
	}
	return stream(ctx, db, page.SQL, nil, sink)
}

func stream(ctx context.Context, db *sql.DB, q string, args []any, sink RowSink) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err == nil {
		err = each(rows, sink.Header, sink.Row)
	}
	if err != nil {
		return engineError("Failed to read the rows.", err)
	}
	return nil
}
