package mongodb

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/Watts-Argentina/anvil/internal/editing"
	"github.com/Watts-Argentina/anvil/internal/query"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// typeName is the DBType of a BSON type. The names fit what the grid
// expects: "datetime" is edited with the calendar and "binary" is
// read-only.
func typeName(t bson.Type) string {
	switch t {
	case bson.TypeString:
		return "string"
	case bson.TypeInt32:
		return "int"
	case bson.TypeInt64:
		return "long"
	case bson.TypeDouble:
		return "double"
	case bson.TypeDecimal128:
		return "decimal"
	case bson.TypeBoolean:
		return "bool"
	case bson.TypeObjectID:
		return "objectId"
	case bson.TypeDateTime:
		return "datetime"
	case bson.TypeEmbeddedDocument:
		return "object"
	case bson.TypeArray:
		return "array"
	case bson.TypeBinary:
		return "binary"
	case bson.TypeNull:
		return "null"
	case bson.TypeTimestamp:
		return "ts" // not "timestamp": that would be edited as a date
	}
	return t.String()
}

// convert turns a field into a grid cell. Raw keeps the BSON value, which
// identifies the document (_id) and gives the type back when editing.
// Documents, arrays and the rarer types are shown as relaxed Extended JSON.
func convert(v bson.RawValue) query.Value {
	switch v.Type {
	case bson.TypeNull, bson.TypeUndefined:
		return query.Value{Null: true, Raw: v}
	case bson.TypeString:
		return query.Value{Text: v.StringValue(), Raw: v}
	case bson.TypeInt32:
		return query.Value{Text: strconv.FormatInt(int64(v.Int32()), 10), Raw: v}
	case bson.TypeInt64:
		return query.Value{Text: strconv.FormatInt(v.Int64(), 10), Raw: v}
	case bson.TypeDouble:
		return query.Value{Text: strconv.FormatFloat(v.Double(), 'g', -1, 64), Raw: v}
	case bson.TypeDecimal128:
		return query.Value{Text: v.Decimal128().String(), Raw: v}
	case bson.TypeBoolean:
		return query.Value{Text: strconv.FormatBool(v.Boolean()), Raw: v}
	case bson.TypeObjectID:
		return query.Value{Text: v.ObjectID().Hex(), Raw: v}
	case bson.TypeDateTime:
		return query.Value{Text: query.Text(v.Time().UTC()), Raw: v}
	case bson.TypeBinary:
		_, data := v.Binary()
		return query.Value{Blob: true, Text: fmt.Sprintf("<BINARY %d B>", len(data)), Raw: v}
	}
	return query.Value{Text: extJSON(v), Raw: v}
}

// extJSON is the relaxed Extended JSON of a value.
func extJSON(v bson.RawValue) string {
	b, err := bson.MarshalExtJSON(bson.D{{Key: "v", Value: v}}, false, false)
	if err != nil {
		return v.String()
	}
	// {"v":…}
	return string(bytes.TrimSuffix(bytes.TrimPrefix(b, []byte(`{"v":`)), []byte("}")))
}

// parse turns an edited cell into a BSON value of type typ (a typeName):
// the original value's type, or else the column's. NULL is BSON null. An
// unknown or mixed type takes the text as Extended JSON if it is valid
// ({…}, […], numbers, true) and as a string otherwise.
func parse(typ string, v editing.Value) (any, error) {
	if v.Null {
		return nil, nil
	}
	s := v.Text
	bad := func(err error) (any, error) {
		return nil, fmt.Errorf("%q is not a valid %s", s, typ)
	}
	switch typ {
	case "string":
		return s, nil
	case "int":
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 32)
		if err != nil {
			return bad(err)
		}
		return int32(n), nil
	case "long":
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return bad(err)
		}
		return n, nil
	case "double":
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return bad(err)
		}
		return f, nil
	case "decimal":
		d, err := bson.ParseDecimal128(strings.TrimSpace(s))
		if err != nil {
			return bad(err)
		}
		return d, nil
	case "bool":
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return bad(err)
		}
		return b, nil
	case "objectId":
		id, err := bson.ObjectIDFromHex(strings.TrimSpace(s))
		if err != nil {
			return bad(err)
		}
		return id, nil
	case "datetime":
		tm, ok := editing.ParseTemporal("datetime", s)
		if !ok {
			return bad(nil)
		}
		return bson.NewDateTimeFromTime(tm), nil
	case "object", "array", "ts", "regex", "javascript":
		rv, err := parseExtJSON(s)
		if err != nil {
			return bad(err)
		}
		return rv, nil
	}
	if rv, err := parseExtJSON(s); err == nil {
		return rv, nil
	}
	return s, nil
}

// parseExtJSON parses one relaxed Extended JSON value.
func parseExtJSON(s string) (bson.RawValue, error) {
	var doc bson.Raw
	if err := bson.UnmarshalExtJSON([]byte(`{"v":`+s+`}`), false, &doc); err != nil {
		return bson.RawValue{}, err
	}
	return doc.LookupErr("v")
}

// cellType is the type an edited cell is saved with: the type of the
// value it had, or the column's if it had none.
func cellType(orig query.Value, colType string) string {
	if rv, ok := orig.Raw.(bson.RawValue); ok && rv.Type != bson.TypeNull && rv.Type != bson.TypeUndefined {
		return typeName(rv.Type)
	}
	return colType
}
