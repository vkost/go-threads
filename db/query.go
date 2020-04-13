package db

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Query is a json-seriable query representation
type Query struct {
	Ands  []*Criterion
	Ors   []*Query
	Sort  Sort
	Index string
}

// Criterion represents a restriction on a field
type Criterion struct {
	FieldPath string
	Operation Operation
	Value     Value
	query     *Query
}

// Value models a single value in JSON
type Value struct {
	String *string
	Bool   *bool
	Float  *float64
}

// Sort represents a sort order on a field
type Sort struct {
	FieldPath string
	Desc      bool
}

// Operation models comparison operators
type Operation int

const (
	// Eq is "equals"
	Eq = Operation(eq)
	// Ne is "not equal to"
	Ne = Operation(ne)
	// Gt is "greater than"
	Gt = Operation(gt)
	// Lt is "less than"
	Lt = Operation(lt)
	// Ge is "greater than or equal to"
	Ge = Operation(ge)
	// Le is "less than or equal to"
	Le = Operation(le)
)

var (
	// ErrInvalidSortingField is returned when a query sorts a result by a
	// non-existent field in the collection schema.
	ErrInvalidSortingField = errors.New("sorting field doesn't correspond to instance type")
)

// Where starts to create a query condition for a field
func Where(field string) *Criterion {
	return &Criterion{
		FieldPath: field,
	}
}

// OrderBy specify ascending order for the query results.
func OrderBy(field string) *Query {
	q := &Query{}
	q.Sort.FieldPath = field
	q.Sort.Desc = false
	return q
}

// OrderByDesc specify descending order for the query results.
func OrderByDesc(field string) *Query {
	q := &Query{}
	q.Sort.FieldPath = field
	q.Sort.Desc = true
	return q
}

// And concatenates a new condition in an existing field.
func (q *Query) And(field string) *Criterion {
	return &Criterion{
		FieldPath: field,
		query:     q,
	}
}

// UseIndex specifies the index to use when running this query
func (q *Query) UseIndex(path string) *Query {
	q.Index = path
	return q
}

// Or concatenates a new condition that is sufficient
// for an instance to satisfy, independant of the current Query.
// Has left-associativity as: (a And b) Or c
func (q *Query) Or(orQuery *Query) *Query {
	q.Ors = append(q.Ors, orQuery)
	return q
}

// OrderBy specify ascending order for the query results.
// On multiple calls, only the last one is considered.
func (q *Query) OrderBy(field string) *Query {
	q.Sort.FieldPath = field
	q.Sort.Desc = false
	return q
}

// OrderByDesc specify descending order for the query results.
// On multiple calls, only the last one is considered.
func (q *Query) OrderByDesc(field string) *Query {
	q.Sort.FieldPath = field
	q.Sort.Desc = true
	return q
}

// Criterion helpers

// Eq is an equality operator against a field
func (c *Criterion) Eq(value interface{}) *Query {
	return c.createcriterion(Eq, value)
}

// Ne is a not equal operator against a field
func (c *Criterion) Ne(value interface{}) *Query {
	return c.createcriterion(Ne, value)
}

// Gt is a greater operator against a field
func (c *Criterion) Gt(value interface{}) *Query {
	return c.createcriterion(Gt, value)
}

// Lt is a less operation against a field
func (c *Criterion) Lt(value interface{}) *Query {
	return c.createcriterion(Lt, value)
}

// Ge is a greater or equal operator against a field
func (c *Criterion) Ge(value interface{}) *Query {
	return c.createcriterion(Ge, value)
}

// Le is a less or equal operator against a field
func (c *Criterion) Le(value interface{}) *Query {
	return c.createcriterion(Le, value)
}

func createValue(value interface{}) Value {
	s, ok := value.(string)
	if ok {
		return Value{String: &s}
	}
	b, ok := value.(bool)
	if ok {
		return Value{Bool: &b}
	}
	f, ok := value.(float64)
	if ok {
		return Value{Float: &f}
	}
	sp, ok := value.(*string)
	if ok {
		return Value{String: sp}
	}
	bp, ok := value.(*bool)
	if ok {
		return Value{Bool: bp}
	}
	fp, ok := value.(*float64)
	if ok {
		return Value{Float: fp}
	}
	return Value{}
}

func (c *Criterion) createcriterion(op Operation, value interface{}) *Query {
	c.Operation = op
	c.Value = createValue(value)
	if c.query == nil {
		c.query = &Query{}
	}
	c.query.Ands = append(c.query.Ands, c)
	return c.query
}

// Find queries for instances by Query
func (t *Txn) Find(q *Query) ([][]byte, error) {
	if q == nil {
		q = &Query{}
	}
	txn, err := t.collection.db.datastore.NewTransaction(true)
	if err != nil {
		return nil, fmt.Errorf("error building internal query: %v", err)
	}
	defer txn.Discard()
	iter := newIterator(txn, t.collection.BaseKey(), q)
	defer iter.Close()

	var values []MarshaledResult
	for {
		res, ok := iter.NextSync()
		if !ok {
			break
		}
		values = append(values, res)
	}

	if q.Sort.FieldPath != "" {
		var wrongField, cantCompare bool
		sort.Slice(values, func(i, j int) bool {
			fieldI, err := traverseFieldPathMap(values[i].MarshaledValue, q.Sort.FieldPath)
			if err != nil {
				wrongField = true
				return false
			}
			fieldJ, err := traverseFieldPathMap(values[j].MarshaledValue, q.Sort.FieldPath)
			if err != nil {
				wrongField = true
				return false
			}
			res, err := compare(fieldI.Interface(), fieldJ.Interface())
			if err != nil {
				cantCompare = true
				return false
			}
			if q.Sort.Desc {
				res *= -1
			}
			return res < 0
		})
		if wrongField {
			return nil, ErrInvalidSortingField
		}
		if cantCompare {
			panic("can't compare while sorting")
		}
	}

	res := make([][]byte, len(values))
	for i := range values {
		res[i] = values[i].Value
	}

	return res, nil
}

func (q *Query) match(v map[string]interface{}) (bool, error) {
	if q == nil {
		panic("query can't be nil")
	}

	andOk := true
	for _, c := range q.Ands {
		fieldRes, err := traverseFieldPathMap(v, c.FieldPath)
		if err != nil {
			return false, err
		}
		ok, err := c.match(fieldRes)
		if err != nil {
			return false, err
		}
		andOk = andOk && ok
		if !andOk {
			break
		}
	}
	if andOk {
		return true, nil
	}

	for _, q := range q.Ors {
		ok, err := q.match(v)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}

	return false, nil
}

func compareValue(value interface{}, critVal Value) (int, error) {
	if critVal.String != nil {
		s, ok := value.(string)
		if !ok {
			return 0, &errTypeMismatch{value, critVal}
		}
		return strings.Compare(s, *critVal.String), nil
	}
	if critVal.Bool != nil {
		b, ok := value.(bool)
		if !ok {
			return 0, &errTypeMismatch{value, critVal}
		}
		if *critVal.Bool == b {
			return 0, nil
		}
		return -1, nil
	}
	if critVal.Float != nil {
		f, ok := value.(float64)
		if !ok {
			return 0, &errTypeMismatch{value, critVal}
		}
		if f == *critVal.Float {
			return 0, nil
		}
		if f < *critVal.Float {
			return -1, nil
		}
		return 1, nil
	}
	log.Fatalf("no underlying value for criterion was provided")
	return 0, nil
}

func (c *Criterion) match(value reflect.Value) (bool, error) {
	valueInterface := value.Interface()
	result, err := compareValue(valueInterface, c.Value)
	if err != nil {
		return false, err
	}
	switch c.Operation {
	case Eq:
		return result == 0, nil
	case Ne:
		return result != 0, nil
	case Gt:
		return result > 0, nil
	case Lt:
		return result < 0, nil
	case Le:
		return result < 0 || result == 0, nil
	case Ge:
		return result > 0 || result == 0, nil
	default:
		panic("invalid operation")
	}

}

func traverseFieldPathMap(value map[string]interface{}, fieldPath string) (reflect.Value, error) {
	fields := strings.Split(fieldPath, ".")

	var curr interface{}
	curr = value
	for i := range fields {
		m, ok := curr.(map[string]interface{})
		if !ok {
			return reflect.Value{}, fmt.Errorf("instance field %s doesn't exist in type %s", fieldPath, value)
		}
		v, ok := m[fields[i]]
		if !ok {
			return reflect.Value{}, fmt.Errorf("instance field %s doesn't exist in type %s", fieldPath, value)
		}
		curr = v
	}
	return reflect.ValueOf(curr), nil
}