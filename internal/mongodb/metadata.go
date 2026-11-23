package mongodb

import (
	"cmp"
	"context"
	"slices"

	"github.com/Watts-Argentina/anvil/internal/drivers"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// sampleSize is how many documents Columns reads to infer the fields.
const sampleSize = 100

// ListDatabases returns the databases the user can read.
func ListDatabases(ctx context.Context, client *mongo.Client) ([]string, error) {
	dbs, err := client.ListDatabaseNames(ctx, bson.D{}, options.ListDatabases().SetAuthorizedDatabases(true))
	if err != nil {
		return nil, err
	}
	slices.Sort(dbs)
	return dbs, nil
}

// ListCollections returns the collections and views of db, by name. Views
// are read-only (IsView).
func ListCollections(ctx context.Context, db *mongo.Database) ([]drivers.Table, error) {
	specs, err := db.ListCollectionSpecifications(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	tables := make([]drivers.Table, 0, len(specs))
	for _, s := range specs {
		tables = append(tables, drivers.Table{Name: s.Name, IsView: s.Type == "view"})
	}
	slices.SortFunc(tables, func(a, b drivers.Table) int { return cmp.Compare(a.Name, b.Name) })
	return tables, nil
}

// Columns infers the top-level fields from a sample of documents: _id
// first (the primary key), then in order of appearance. DBType is the most
// common BSON type of the field (typeName). A collection has no schema, so
// fields missing from the sample are still shown when a page has them.
func Columns(ctx context.Context, coll *mongo.Collection) ([]drivers.Column, error) {
	cur, err := coll.Find(ctx, bson.D{}, options.Find().SetLimit(sampleSize))
	if err != nil {
		return nil, err
	}
	var docs []bson.Raw
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return fields(docs), nil
}

// fields returns the columns of docs, as Columns describes.
func fields(docs []bson.Raw) []drivers.Column {
	names := []string{"_id"}
	counts := map[string]map[string]int{}
	for _, d := range docs {
		elems, err := d.Elements()
		if err != nil {
			continue
		}
		for _, e := range elems {
			name, v := e.Key(), e.Value()
			if counts[name] == nil {
				counts[name] = map[string]int{}
				if name != "_id" {
					names = append(names, name)
				}
			}
			if v.Type != bson.TypeNull {
				counts[name][typeName(v.Type)]++
			}
		}
	}

	cols := make([]drivers.Column, len(names))
	for i, name := range names {
		typ, best := "", 0
		for t, n := range counts[name] {
			if n > best || n == best && t < typ {
				typ, best = t, n
			}
		}
		if name == "_id" {
			if typ == "" {
				typ = "objectId"
			}
			cols[i] = drivers.Column{Name: name, DBType: typ, PK: true, PKOrdinal: 1}
			continue
		}
		cols[i] = drivers.Column{Name: name, DBType: typ, Nullable: true}
	}
	return cols
}
