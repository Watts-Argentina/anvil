package query

import (
	"context"
	"errors"
	"testing"

	"github.com/Watts-Argentina/anvil/pkg/appErrors"
)

type collect struct {
	cols []Column
	rows [][]Value
}

func (c *collect) Header(cols []Column) error { c.cols = cols; return nil }
func (c *collect) Row(row []Value) error      { c.rows = append(c.rows, row); return nil }

func TestStreamTable(t *testing.T) {
	db := newDB(t)
	src := source(t, db, "items")
	src.Filter = []Cond{{Column: "name", Value: "item 7"}}
	var c collect
	if err := StreamTable(context.Background(), src, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.cols) != 4 || len(c.rows) != 1 || c.rows[0][0].Text != "70" {
		t.Fatalf("cols=%v rows=%v", c.cols, c.rows)
	}

	src.Filter = nil
	c = collect{}
	if err := StreamTable(context.Background(), src, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.rows) != 25 || c.rows[24][0].Text != "250" {
		t.Fatalf("got %d rows, last %v", len(c.rows), c.rows[len(c.rows)-1])
	}
}

func TestStreamRows(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	plan, err := NewPlan(source(t, db, "logs").Driver, "select msg from logs order by msg", false)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(ctx, db, plan, 10)
	if err != nil {
		t.Fatal(err)
	}
	var c collect
	if err := StreamRows(ctx, db, *res.Rows, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.rows) != 25 || c.rows[24][0].Text != "log 25" {
		t.Fatalf("got %d rows", len(c.rows))
	}

	page := *res.Rows
	page.Repeatable = false
	var ae *appErrors.AppError
	if err := StreamRows(ctx, db, page, &c); !errors.As(err, &ae) {
		t.Fatalf("err = %v", err)
	}
}

var errDisk = errors.New("disk full")

type failing struct{ collect }

func (f *failing) Row([]Value) error { return errDisk }

func TestStreamSinkError(t *testing.T) {
	db := newDB(t)
	err := StreamTable(context.Background(), source(t, db, "logs"), &failing{})
	if !errors.Is(err, errDisk) {
		t.Fatalf("err = %v", err)
	}
}
