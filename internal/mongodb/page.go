package mongodb

import (
	"context"
	"errors"
	"slices"

	"github.com/Watts-Argentina/anvil/internal/drivers"
	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/pkg/appErrors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var byID = bson.D{{Key: "_id", Value: 1}}

// LoadPage loads a page of the collection in _id order, like
// query.LoadTablePage. The columns are cols plus the fields of the page
// that cols does not have.
//
// shortcut: skip-based pagination, O(offset) on the server; switch to an
// _id keyset if deep pages on big collections get slow.
func LoadPage(ctx context.Context, coll *mongo.Collection, cols []drivers.Column, cur query.Page, dir query.Dir, size int) (query.Page, error) {
	index := 0
	switch dir {
	case query.Next:
		index = cur.Index + 1
	case query.Prev:
		index = max(cur.Index-1, 0)
	case query.Reload:
		index = cur.Index
	}
	opts := options.Find().SetSort(byID).SetSkip(int64(index * size)).SetLimit(int64(size + 1))
	c, err := coll.Find(ctx, bson.D{}, opts)
	if err != nil {
		return query.Page{}, engineError("Failed to load documents.", err)
	}
	var docs []bson.Raw
	if err := c.All(ctx, &docs); err != nil {
		return query.Page{}, engineError("Failed to load documents.", err)
	}
	more := len(docs) > size
	if more {
		docs = docs[:size]
	}
	return query.Page{Result: result(cols, docs), Index: index, Size: size, HasNext: more}, nil
}

// result builds the grid rows of docs with the columns cols plus the
// fields they have that cols does not. A missing field is NULL.
func result(cols []drivers.Column, docs []bson.Raw) query.Result {
	var res query.Result
	for _, c := range cols {
		res.Columns = append(res.Columns, query.Column{Name: c.Name, DBType: c.DBType})
	}
	for _, c := range fields(docs) {
		if !slices.ContainsFunc(res.Columns, func(rc query.Column) bool { return rc.Name == c.Name }) {
			res.Columns = append(res.Columns, query.Column{Name: c.Name, DBType: c.DBType})
		}
	}
	for _, d := range docs {
		res.Rows = append(res.Rows, row(res.Columns, d))
	}
	return res
}

func row(cols []query.Column, d bson.Raw) []query.Value {
	out := make([]query.Value, len(cols))
	for i, c := range cols {
		if v, err := d.LookupErr(c.Name); err == nil {
			out[i] = convert(v)
		} else {
			out[i] = query.Value{Null: true}
		}
	}
	return out
}

// Count counts the collection's documents (on demand, SPEC §6.2).
func Count(ctx context.Context, coll *mongo.Collection) (int64, error) {
	n, err := coll.CountDocuments(ctx, bson.D{})
	if err != nil {
		return 0, engineError("Failed to count documents.", err)
	}
	return n, nil
}

// Stream sends every document of the collection to sink in _id order
// (export, SPEC §9). The header has every field of the collection, read
// first with an aggregation, so no field is lost in the export.
func Stream(ctx context.Context, coll *mongo.Collection, cols []drivers.Column, sink query.RowSink) error {
	all, err := allFields(ctx, coll, cols)
	if err != nil {
		return engineError("Failed to read the documents.", err)
	}
	c, err := coll.Find(ctx, bson.D{}, options.Find().SetSort(byID))
	if err != nil {
		return engineError("Failed to read the documents.", err)
	}
	return streamCursor(ctx, c, all, sink)
}

// allFields returns cols plus the other top-level fields of the collection.
func allFields(ctx context.Context, coll *mongo.Collection, cols []drivers.Column) ([]query.Column, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$project", Value: bson.D{{Key: "k", Value: bson.D{{Key: "$objectToArray", Value: "$$ROOT"}}}}}},
		{{Key: "$unwind", Value: "$k"}},
		{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$k.k"}}}},
		{{Key: "$sort", Value: byID}},
	}
	c, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	var names []struct {
		ID string `bson:"_id"`
	}
	if err := c.All(ctx, &names); err != nil {
		return nil, err
	}
	out := make([]query.Column, 0, len(cols)+len(names))
	for _, col := range cols {
		out = append(out, query.Column{Name: col.Name, DBType: col.DBType})
	}
	for _, n := range names {
		if !slices.ContainsFunc(out, func(c query.Column) bool { return c.Name == n.ID }) {
			out = append(out, query.Column{Name: n.ID})
		}
	}
	return out, nil
}

// streamCursor sends the header and then every document of c.
func streamCursor(ctx context.Context, c *mongo.Cursor, cols []query.Column, sink query.RowSink) error {
	defer c.Close(ctx)
	if err := sink.Header(cols); err != nil {
		return err
	}
	for c.Next(ctx) {
		if err := sink.Row(row(cols, c.Current)); err != nil {
			return err
		}
	}
	if err := c.Err(); err != nil {
		return engineError("Failed to read the documents.", err)
	}
	return nil
}

// engineError wraps a server error for the UI.
func engineError(title string, err error) error {
	if errors.Is(err, context.Canceled) {
		return appErrors.Error("Query cancelled.", "The query was cancelled.", err)
	}
	return appErrors.Error(title, err.Error(), err)
}
