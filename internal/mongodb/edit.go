package mongodb

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Watts-Argentina/anvil/internal/drivers"
	"github.com/Watts-Argentina/anvil/internal/editing"
	"github.com/Watts-Argentina/anvil/internal/query"
	"github.com/Watts-Argentina/anvil/pkg/appErrors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Write is the operation of a pending change (SPEC §7): insertOne,
// updateOne ($set) or deleteOne, always by _id.
type Write struct {
	Change editing.Change
	Label  string // "update of row 3", to report errors
	filter bson.D // _id of the document (update, delete)
	doc    bson.D // the inserted document or the $set fields
}

// Build turns the changes into writes, in order. page is the page the
// changes refer to: Change.Row indexes page.Rows. A value that does not
// parse as its field's type is reported here, before anything is saved.
func Build(cols []drivers.Column, page query.Result, changes []editing.Change) ([]Write, error) {
	idCol := -1
	for i, c := range page.Columns {
		if c.Name == "_id" {
			idCol = i
		}
	}
	colType := map[string]string{}
	for _, c := range cols {
		colType[c.Name] = c.DBType
	}

	writes := make([]Write, 0, len(changes))
	for _, ch := range changes {
		w := Write{Change: ch}
		values := func(orig []query.Value) (bson.D, error) {
			var doc bson.D
			for _, c := range cols { // column order, not map order
				v, ok := ch.Values[c.Name]
				if !ok || c.Name == "_id" && ch.Kind == editing.Insert && v == (editing.Value{}) {
					continue // an empty _id lets the server generate it
				}
				typ := c.DBType
				if orig != nil {
					if i := columnIndex(page, c.Name); i >= 0 {
						typ = cellType(orig[i], typ)
					}
				}
				bv, err := parse(typ, v)
				if err != nil {
					return nil, invalidValue(w.Label, c.Name, err)
				}
				doc = append(doc, bson.E{Key: c.Name, Value: bv})
			}
			return doc, nil
		}

		var err error
		switch ch.Kind {
		case editing.Insert:
			w.Label = "insert of new row " + strconv.Itoa(ch.Row)
			w.doc, err = values(nil)
		default:
			w.Label = map[editing.Kind]string{editing.Update: "update", editing.Delete: "delete"}[ch.Kind] + " of row " + strconv.Itoa(ch.Row+1)
			if idCol < 0 || ch.Row < 0 || ch.Row >= len(page.Rows) {
				return nil, appErrors.Error("Rows cannot be identified.", "The documents of this page have no _id.", fmt.Errorf("no _id for row %d", ch.Row))
			}
			orig := page.Rows[ch.Row]
			w.filter = bson.D{{Key: "_id", Value: orig[idCol].Raw}}
			if ch.Kind == editing.Update {
				w.doc, err = values(orig)
			}
		}
		if err != nil {
			return nil, err
		}
		writes = append(writes, w)
	}
	return writes, nil
}

func columnIndex(page query.Result, name string) int {
	for i, c := range page.Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}

func invalidValue(label, field string, err error) error {
	return appErrors.Error("Invalid value.", fmt.Sprintf("In the %s, %s: %v.", label, field, err), err)
}

// Preview is the write as a mongosh call, only to show it in the review.
func (w Write) Preview(coll string) string {
	js := func(v any) string {
		b, err := bson.MarshalExtJSON(v, false, false)
		if err != nil {
			return "?"
		}
		return string(b)
	}
	prefix := "db.getCollection(" + strconv.Quote(coll) + ")."
	switch w.Change.Kind {
	case editing.Insert:
		return prefix + "insertOne(" + js(w.doc) + ")"
	case editing.Update:
		return prefix + "updateOne(" + js(w.filter) + ", " + js(bson.D{{Key: "$set", Value: w.doc}}) + ")"
	}
	return prefix + "deleteOne(" + js(w.filter) + ")"
}

// Apply runs the writes. With transactions (Transactions) it is all or
// nothing, like editing.Apply. A standalone server has none: the writes run
// one by one and the error says how many were saved before the failure.
// Every update and delete must find exactly one document (SPEC §7.3).
func Apply(ctx context.Context, coll *mongo.Collection, writes []Write, transactions bool) error {
	if !transactions {
		done, err := applyAll(ctx, coll, writes)
		if err != nil {
			return appErrors.Error("Changes partially saved.",
				fmt.Sprintf("%v. This server does not support transactions: %d of %d changes were saved before the failure; reload the page.", err, done, len(writes)), err)
		}
		return nil
	}

	sess, err := coll.Database().Client().StartSession()
	if err != nil {
		return appErrors.Error("Changes not saved.", "The transaction could not be started: "+err.Error(), err)
	}
	defer sess.EndSession(context.Background())
	_, err = sess.WithTransaction(ctx, func(ctx context.Context) (any, error) {
		_, err := applyAll(ctx, coll, writes)
		return nil, err
	})
	if err != nil {
		return appErrors.Error("Changes not saved.", fmt.Sprintf("%v. Nothing was saved.", err), err)
	}
	return nil
}

// applyAll runs the writes in order until one fails, and returns how many
// succeeded.
func applyAll(ctx context.Context, coll *mongo.Collection, writes []Write) (int, error) {
	for i, w := range writes {
		var n int64
		var err error
		switch w.Change.Kind {
		case editing.Insert:
			_, err = coll.InsertOne(ctx, w.doc)
			n = 1
		case editing.Update:
			var res *mongo.UpdateResult
			if res, err = coll.UpdateOne(ctx, w.filter, bson.D{{Key: "$set", Value: w.doc}}); err == nil {
				n = res.MatchedCount
			}
		case editing.Delete:
			var res *mongo.DeleteResult
			if res, err = coll.DeleteOne(ctx, w.filter); err == nil {
				n = res.DeletedCount
			}
		}
		if err != nil {
			return i, fmt.Errorf("the %s failed: %w", w.Label, err)
		}
		if n != 1 {
			return i, fmt.Errorf("the %s found no document: it no longer exists", w.Label)
		}
	}
	return len(writes), nil
}
