package aggregation

import (
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/shopspring/decimal"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/types"
)

type anyValueBuffer struct {
	res  interface{}
	expr sql.Expression
}

func NewAnyValueBuffer(child sql.Expression) *anyValueBuffer {
	return &anyValueBuffer{nil, child}
}

// Update implements the AggregationBuffer interface.
func (a *anyValueBuffer) Update(ctx *sql.Context, row sql.Row) error {
	if a.res != nil {
		return nil
	}

	v, err := a.expr.Eval(ctx, row)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}

	a.res = v

	return nil
}

// Eval implements the AggregationBuffer interface.
func (a *anyValueBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	return a.res, nil
}

// Dispose implements the Disposable interface.
func (a *anyValueBuffer) Dispose() {
	expression.Dispose(a.expr)
}

type sumBuffer struct {
	isnil bool
	sum   interface{} // sum is either decimal.Decimal or float64
	expr  sql.Expression
}

func NewSumBuffer(child sql.Expression) *sumBuffer {
	return &sumBuffer{true, float64(0), child}
}

// Update implements the AggregationBuffer interface.
func (m *sumBuffer) Update(ctx *sql.Context, row sql.Row) error {
	v, err := m.expr.Eval(ctx, row)
	if err != nil {
		return err
	}

	if v == nil {
		return nil
	}

	m.PerformSum(ctx, v)

	return nil
}

func (m *sumBuffer) PerformSum(ctx *sql.Context, v interface{}) {
	// decimal.Decimal values are evaluated to string value even though the Literal expr type is Decimal type,
	// so convert it to appropriate Decimal type
	if s, isStr := v.(string); isStr && types.IsDecimal(m.expr.Type()) {
		val, _, err := m.expr.Type().Convert(ctx, s)
		if err == nil {
			v = val
		}
	}

	switch n := v.(type) {
	case decimal.Decimal:
		if m.isnil {
			m.sum = decimal.NewFromInt(0)
			m.isnil = false
		}
		if sum, ok := m.sum.(decimal.Decimal); ok {
			m.sum = sum.Add(n)
		} else {
			m.sum = decimal.NewFromFloat(m.sum.(float64)).Add(n)
		}
	default:
		val, _, err := types.Float64.Convert(ctx, n)
		if err != nil {
			val = float64(0)
		}
		if m.isnil {
			m.sum = float64(0)
			m.isnil = false
		}
		sum, _, err := types.Float64.Convert(ctx, m.sum)
		if err != nil {
			sum = float64(0)
		}
		m.sum = sum.(float64) + val.(float64)
	}
}

// Eval implements the AggregationBuffer interface.
func (m *sumBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	if m.isnil {
		return nil, nil
	}
	return m.sum, nil
}

// Dispose implements the Disposable interface.
func (m *sumBuffer) Dispose() {
	expression.Dispose(m.expr)
}

type lastBuffer struct {
	val  interface{}
	expr sql.Expression
}

func NewLastBuffer(child sql.Expression) *lastBuffer {
	const (
		sum  = float64(0)
		rows = int64(0)
	)

	return &lastBuffer{nil, child}
}

// Update implements the AggregationBuffer interface.
func (l *lastBuffer) Update(ctx *sql.Context, row sql.Row) error {
	v, err := l.expr.Eval(ctx, row)
	if err != nil {
		return err
	}

	if v == nil {
		return nil
	}

	l.val = v

	return nil
}

// Eval implements the AggregationBuffer interface.
func (l *lastBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	return l.val, nil
}

// Dispose implements the Disposable interface.
func (l *lastBuffer) Dispose() {
	expression.Dispose(l.expr)
}

type avgBuffer struct {
	sum  *sumBuffer // sum is either decimal.Decimal or float64
	rows int64
	expr sql.Expression
}

func NewAvgBuffer(child sql.Expression) *avgBuffer {
	const (
		rows = int64(0)
	)

	return &avgBuffer{NewSumBuffer(child), rows, child}
}

// Update implements the AggregationBuffer interface.
func (a *avgBuffer) Update(ctx *sql.Context, row sql.Row) error {
	v, err := a.expr.Eval(ctx, row)
	if err != nil {
		return err
	}

	if v == nil {
		return nil
	}

	a.sum.PerformSum(ctx, v)
	a.rows += 1

	return nil
}

// Eval implements the AggregationBuffer interface.
func (a *avgBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	sum, err := a.sum.Eval(ctx)
	if err != nil {
		return nil, err
	}
	// This case is triggered when no rows exist.
	switch s := sum.(type) {
	case float64:
		if s == 0 && a.rows == 0 {
			return nil, nil
		}

		if a.rows == 0 {
			return float64(0), nil
		}

		return s / float64(a.rows), nil
	case decimal.Decimal:
		if s.IsZero() && a.rows == 0 {
			return nil, nil
		}
		if a.rows == 0 {
			return decimal.NewFromInt(0), nil
		}
		scale := (s.Exponent() * -1) + 4
		return s.DivRound(decimal.NewFromInt(a.rows), scale), nil
	}
	return nil, nil
}

// Dispose implements the Disposable interface.
func (a *avgBuffer) Dispose() {
	expression.Dispose(a.expr)
}

type bitAndBuffer struct {
	res  uint64
	rows uint64
	expr sql.Expression
}

func NewBitAndBuffer(child sql.Expression) *bitAndBuffer {
	const (
		res  = ^uint64(0) // bitwise not xor, so 0xffff...
		rows = uint64(0)
	)

	return &bitAndBuffer{res, rows, child}
}

// Update implements the AggregationBuffer interface.
func (b *bitAndBuffer) Update(ctx *sql.Context, row sql.Row) error {
	v, err := b.expr.Eval(ctx, row)
	if err != nil {
		return err
	}

	if v == nil {
		return nil
	}

	v, _, err = types.Uint64.Convert(ctx, v)
	if err != nil {
		v = uint64(0)
	}

	b.res &= v.(uint64)
	b.rows += 1

	return nil
}

// Eval implements the AggregationBuffer interface.
func (b *bitAndBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	return b.res, nil
}

// Dispose implements the Disposable interface.
func (b *bitAndBuffer) Dispose() {
	expression.Dispose(b.expr)
}

type bitOrBuffer struct {
	res  uint64
	rows uint64
	expr sql.Expression
}

func NewBitOrBuffer(child sql.Expression) *bitOrBuffer {
	const (
		res  = uint64(0)
		rows = uint64(0)
	)

	return &bitOrBuffer{res, rows, child}
}

// Update implements the AggregationBuffer interface.
func (b *bitOrBuffer) Update(ctx *sql.Context, row sql.Row) error {
	v, err := b.expr.Eval(ctx, row)
	if err != nil {
		return err
	}

	if v == nil {
		return nil
	}

	v, _, err = types.Uint64.Convert(ctx, v)
	if err != nil {
		v = uint64(0)
	}

	b.res |= v.(uint64)
	b.rows += 1

	return nil
}

// Eval implements the AggregationBuffer interface.
func (b *bitOrBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	return b.res, nil
}

// Dispose implements the Disposable interface.
func (b *bitOrBuffer) Dispose() {
	expression.Dispose(b.expr)
}

type bitXorBuffer struct {
	res  uint64
	rows uint64
	expr sql.Expression
}

func NewBitXorBuffer(child sql.Expression) *bitXorBuffer {
	const (
		res  = uint64(0)
		rows = uint64(0)
	)

	return &bitXorBuffer{res, rows, child}
}

// Update implements the AggregationBuffer interface.
func (b *bitXorBuffer) Update(ctx *sql.Context, row sql.Row) error {
	v, err := b.expr.Eval(ctx, row)
	if err != nil {
		return err
	}

	if v == nil {
		return nil
	}

	v, _, err = types.Uint64.Convert(ctx, v)
	if err != nil {
		v = uint64(0)
	}

	b.res ^= v.(uint64)
	b.rows += 1

	return nil
}

// Eval implements the AggregationBuffer interface.
func (b *bitXorBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	// This case is triggered when no rows exist.
	if b.res == 0 && b.rows == 0 {
		return uint64(0), nil
	}

	if b.rows == 0 {
		return uint64(0), nil
	}

	return b.res, nil
}

// Dispose implements the Disposable interface.
func (b *bitXorBuffer) Dispose() {
	expression.Dispose(b.expr)
}

// countDistinctBuffer counts the distinct non-NULL value tuples of its
// expressions within one group. A buffer is created per group and updated
// from a single goroutine, so its scratch state needs no locking.
type countDistinctBuffer struct {
	// seen holds the hash of every distinct value tuple observed so far.
	seen map[uint64]struct{}
	// exprs are the COUNT(DISTINCT ...) arguments (or a single Star).
	exprs []sql.Expression
	// digest is reused (Reset per row) to hash each value tuple.
	digest *xxhash.Digest
	// buf is reused scratch space for converting each value to text.
	buf []byte
	// vals is reused to hold the evaluated expressions of the current row
	// (non-Star path only).
	vals sql.Row
}

// comma is the separator written after each value when hashing a tuple.
var comma = []byte{','}

func NewCountDistinctBuffer(children []sql.Expression) *countDistinctBuffer {
	return &countDistinctBuffer{
		seen:   make(map[uint64]struct{}),
		exprs:  children,
		digest: xxhash.New(),
		vals:   make(sql.Row, len(children)),
	}
}

// Update implements the AggregationBuffer interface.
func (c *countDistinctBuffer) Update(ctx *sql.Context, row sql.Row) error {
	var value sql.Row
	if len(c.exprs) == 0 {
		return fmt.Errorf("no expressions")
	}
	if _, ok := c.exprs[0].(*expression.Star); ok {
		value = row
	} else {
		for i, expr := range c.exprs {
			v, err := expr.Eval(ctx, row)
			if err != nil {
				return err
			}
			// skip nil values
			if v == nil {
				return nil
			}
			c.vals[i] = v
		}
		value = c.vals
	}

	h, ok, err := c.hashRow(ctx, value)
	if err != nil || !ok {
		return err
	}
	c.seen[h] = struct{}{}

	return nil
}

// hashRow hashes the text form of each value followed by a comma, which is
// the same byte stream as concatenating types.Text.Convert(v) + "," for every
// value. It reports false (and no hash) when any value is nil, in which case
// the row is skipped.
//
// types.Text.Convert is ConvertToBytes(ctx, v, types.Text, nil) followed by a
// string conversion, so the bytes, the length limit and the invalid-UTF-8
// error are the same. The one difference: Text.Convert returns a Text-sized
// sql.StringWrapper unchanged, which the previous string-concatenating
// implementation then rejected with "count distinct unable to hash value";
// ConvertToBytes unwraps it, so such values are now hashed by their contents.
func (c *countDistinctBuffer) hashRow(ctx *sql.Context, vals sql.Row) (uint64, bool, error) {
	c.digest.Reset()
	for _, v := range vals {
		// skip nil values
		if v == nil {
			return 0, false, nil
		}
		b, err := types.ConvertToBytes(ctx, v, types.Text, c.buf[:0])
		if err != nil {
			return 0, false, err
		}
		if ownsConvertedBytes(v) && cap(b) > cap(c.buf) {
			// b was appended to c.buf and grew it; keep the larger backing
			// array so the growth is reused. An invalid NullDecimal yields a
			// nil b, which must not replace the scratch space.
			c.buf = b
		}
		if _, err := c.digest.Write(b); err != nil {
			return 0, false, err
		}
		if _, err := c.digest.Write(comma); err != nil {
			return 0, false, err
		}
	}
	return c.digest.Sum64(), true, nil
}

// ownsConvertedBytes reports whether types.ConvertToBytes appends v's text
// form to its dest argument. For other kinds (e.g. []byte, JSON, byte
// wrappers, geometry) it can return a slice owned by the value itself, which
// must not be retained as scratch space and written into.
func ownsConvertedBytes(v interface{}) bool {
	switch v.(type) {
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64,
		float32, float64, time.Time, decimal.Decimal, decimal.NullDecimal:
		return true
	default:
		return false
	}
}

// Eval implements the AggregationBuffer interface.
func (c *countDistinctBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	return int64(len(c.seen)), nil
}

func (c *countDistinctBuffer) Dispose() {
	for _, e := range c.exprs {
		expression.Dispose(e)
	}
}

type countBuffer struct {
	cnt  int64
	expr sql.Expression
}

func NewCountBuffer(child sql.Expression) *countBuffer {
	return &countBuffer{0, child}
}

// Update implements the AggregationBuffer interface.
func (c *countBuffer) Update(ctx *sql.Context, row sql.Row) error {
	var inc bool
	if _, ok := c.expr.(*expression.Star); ok {
		inc = true
	} else {
		v, err := c.expr.Eval(ctx, row)
		if v != nil {
			inc = true
		}

		if err != nil {
			return err
		}
	}

	if inc {
		c.cnt += 1
	}

	return nil
}

// Eval implements the AggregationBuffer interface.
func (c *countBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	return c.cnt, nil
}

// Dispose implements the Disposable interface.
func (c *countBuffer) Dispose() {
	expression.Dispose(c.expr)
}

type firstBuffer struct {
	val  interface{}
	expr sql.Expression
}

func NewFirstBuffer(child sql.Expression) *firstBuffer {
	return &firstBuffer{nil, child}
}

// Update implements the AggregationBuffer interface.
func (f *firstBuffer) Update(ctx *sql.Context, row sql.Row) error {
	if f.val != nil {
		return nil
	}

	v, err := f.expr.Eval(ctx, row)
	if err != nil {
		return err
	}

	if v == nil {
		return nil
	}

	f.val = v

	return nil
}

// Eval implements the AggregationBuffer interface.
func (f *firstBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	return f.val, nil
}

// Dispose implements the Disposable interface.
func (f *firstBuffer) Dispose() {
	expression.Dispose(f.expr)
}

type maxBuffer struct {
	val  interface{}
	expr sql.Expression
}

func NewMaxBuffer(child sql.Expression) *maxBuffer {
	return &maxBuffer{nil, child}
}

// Update implements the AggregationBuffer interface.
func (m *maxBuffer) Update(ctx *sql.Context, row sql.Row) error {
	v, err := m.expr.Eval(ctx, row)
	if err != nil {
		return err
	}

	if reflect.TypeOf(v) == nil {
		return nil
	}

	if m.val == nil {
		m.val = v
		return nil
	}

	cmp, err := m.expr.Type().Compare(ctx, v, m.val)
	if err != nil {
		return err
	}
	if cmp == 1 {
		m.val = v
	}

	return nil
}

// Eval implements the AggregationBuffer interface.
func (m *maxBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	return m.val, nil
}

// Dispose implements the Disposable interface.
func (m *maxBuffer) Dispose() {
	expression.Dispose(m.expr)
}

type minBuffer struct {
	val  interface{}
	expr sql.Expression
}

func NewMinBuffer(child sql.Expression) *minBuffer {
	return &minBuffer{nil, child}
}

// Update implements the AggregationBuffer interface.
func (m *minBuffer) Update(ctx *sql.Context, row sql.Row) error {
	v, err := m.expr.Eval(ctx, row)
	if err != nil {
		return err
	}

	if reflect.TypeOf(v) == nil {
		return nil
	}

	if m.val == nil {
		m.val = v
		return nil
	}

	cmp, err := m.expr.Type().Compare(ctx, v, m.val)
	if err != nil {
		return err
	}
	if cmp == -1 {
		m.val = v
	}

	return nil
}

// Eval implements the AggregationBuffer interface.
func (m *minBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	return m.val, nil
}

// Dispose implements the Disposable interface.
func (m *minBuffer) Dispose() {
	expression.Dispose(m.expr)
}

type jsonArrayBuffer struct {
	vals []interface{}
	expr sql.Expression
}

func NewJsonArrayBuffer(child sql.Expression) *jsonArrayBuffer {
	return &jsonArrayBuffer{nil, child}
}

// Update implements the AggregationBuffer interface.
func (j *jsonArrayBuffer) Update(ctx *sql.Context, row sql.Row) error {
	v, err := j.expr.Eval(ctx, row)
	if err != nil {
		return err
	}

	// unwrap JSON values
	if js, ok := v.(sql.JSONWrapper); ok {
		v, err = js.ToInterface()
		if err != nil {
			return err
		}
	}

	j.vals = append(j.vals, v)

	return nil
}

// Eval implements the AggregationBuffer interface.
func (j *jsonArrayBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	return types.JSONDocument{Val: j.vals}, nil
}

// Dispose implements the Disposable interface.
func (j *jsonArrayBuffer) Dispose() {
}

type varBaseBuffer struct {
	vals []interface{}
	expr sql.Expression

	count uint64
	mean  float64
	std2  float64
}

// Update implements the AggregationBuffer interface.
func (vb *varBaseBuffer) Update(ctx *sql.Context, row sql.Row) error {
	v, err := vb.expr.Eval(ctx, row)
	if err != nil {
		return err
	}
	v, _, err = types.Float64.Convert(ctx, v)
	if err != nil {
		v = 0.0
		ctx.Warn(1292, "Truncated incorrect DOUBLE value: %s", v)
	}
	if v == nil {
		return nil
	}
	val := v.(float64)

	vb.count += 1
	if vb.count == 1 {
		vb.mean = val
		return nil
	}

	newMean := vb.mean + (val-vb.mean)/float64(vb.count)
	vb.std2 = vb.std2 + (val-vb.mean)*(val-newMean)
	vb.mean = newMean

	return nil
}

// Dispose implements the Disposable interface.
func (vb *varBaseBuffer) Dispose() {}

type stdDevPopBuffer struct {
	varBaseBuffer
}

func NewStdDevPopBuffer(child sql.Expression) *stdDevPopBuffer {
	return &stdDevPopBuffer{
		varBaseBuffer: varBaseBuffer{
			expr: child,
		},
	}
}

// Eval implements the AggregationBuffer interface.
func (s *stdDevPopBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	if s.count == 0 {
		return nil, nil
	}
	return math.Sqrt(s.std2 / float64(s.count)), nil
}

type stdDevSampBuffer struct {
	varBaseBuffer
}

func NewStdDevSampBuffer(child sql.Expression) *stdDevSampBuffer {
	return &stdDevSampBuffer{
		varBaseBuffer: varBaseBuffer{
			expr: child,
		},
	}
}

// Eval implements the AggregationBuffer interface.
func (s *stdDevSampBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	if s.count <= 1 {
		return nil, nil
	}
	return math.Sqrt(s.std2 / float64(s.count-1)), nil
}

type varPopBuffer struct {
	varBaseBuffer
}

func NewVarPopBuffer(child sql.Expression) *varPopBuffer {
	return &varPopBuffer{
		varBaseBuffer: varBaseBuffer{
			expr: child,
		},
	}
}

// Eval implements the AggregationBuffer interface.
func (vp *varPopBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	if vp.count == 0 {
		return nil, nil
	}
	return vp.std2 / float64(vp.count), nil
}

type varSampBuffer struct {
	varBaseBuffer
}

func NewVarSampBuffer(child sql.Expression) *varSampBuffer {
	return &varSampBuffer{
		varBaseBuffer: varBaseBuffer{
			expr: child,
		},
	}
}

// Eval implements the AggregationBuffer interface.
func (vp *varSampBuffer) Eval(ctx *sql.Context) (interface{}, error) {
	if vp.count <= 1 {
		return nil, nil
	}
	return vp.std2 / float64(vp.count-1), nil
}
