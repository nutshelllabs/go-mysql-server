// Copyright 2026 Dolthub, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aggregation

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// countDistinctHashReference is a verbatim copy of the per-row hashing the
// allocation-free countDistinctBuffer.hashRow replaced. ok is false when the
// row is skipped because of a nil value.
func countDistinctHashReference(ctx *sql.Context, vals sql.Row) (h uint64, ok bool, err error) {
	var str string
	for _, val := range vals {
		// skip nil values
		if val == nil {
			return 0, false, nil
		}
		v, _, err := types.Text.Convert(ctx, val)
		if err != nil {
			return 0, false, err
		}
		vv, ok := v.(string)
		if !ok {
			return 0, false, fmt.Errorf("count distinct unable to hash value: %s", err)
		}
		str += vv + ","
	}

	hash := xxhash.New()
	_, err = hash.WriteString(str)
	if err != nil {
		return 0, false, err
	}
	return hash.Sum64(), true, nil
}

func TestCountDistinctHashMatchesReference(t *testing.T) {
	ctx := sql.NewEmptyContext()
	loc := time.FixedZone("X", 5*3600+30*60)
	ts := time.Date(2024, 2, 29, 23, 59, 59, 123456789, time.UTC)
	rows := []sql.Row{
		{},
		{"a"}, {""}, {"", ""}, {"a,b", "c"}, {"a", "b,c"}, {"héllo 世界 🙂"},
		{int(-1), int8(-128), int16(math.MaxInt16)},
		{int32(math.MinInt32), int64(math.MaxInt64)},
		{uint(1), uint8(255), uint16(math.MaxUint16)},
		{uint32(math.MaxUint32), uint64(math.MaxUint64)},
		{1.5, math.Copysign(0, -1), 1e21},
		{math.NaN(), math.Inf(1), math.Inf(-1)},
		{float32(0.1), float32(math.Copysign(0, -1)), float32(1e21)},
		{true, false},
		{ts, ts.In(loc), time.Time{}},
		{decimal.RequireFromString("-1234.5600"), decimal.New(1, 21), decimal.New(-5, -30)},
		{decimal.NullDecimal{Decimal: decimal.RequireFromString("12.50"), Valid: true}},
		{decimal.NullDecimal{}, "x"},
		{[]byte{}, []byte("abc"), []byte{0, 1}},
		{types.JSONDocument{Val: map[string]any{"a": 1.0, "b": []any{"x", nil}}}},
		{"a", nil, "b"},
		{nil},
		{int64(1), "1"},
		{"1", int64(1)},
	}
	buf := NewCountDistinctBuffer([]sql.Expression{expression.NewStar()})
	for i, row := range rows {
		got, gotOK, err := buf.hashRow(ctx, row)
		require.NoError(t, err, "row %d", i)
		want, wantOK, err := countDistinctHashReference(ctx, row)
		require.NoError(t, err, "row %d", i)
		require.Equal(t, wantOK, gotOK, "row %d", i)
		require.Equal(t, want, got, "row %d: %#v", i, row)
	}

	// Error cases must fail the same way on both sides.
	_, _, err := buf.hashRow(ctx, sql.Row{"ok", string("\xff")})
	require.True(t, types.ErrBadCharsetString.Is(err), "%v", err)
	_, _, err = countDistinctHashReference(ctx, sql.Row{"ok", string("\xff")})
	require.True(t, types.ErrBadCharsetString.Is(err), "%v", err)

	long := strings.Repeat("x", 65536)
	_, _, err = buf.hashRow(ctx, sql.Row{long})
	require.True(t, types.ErrLengthBeyondLimit.Is(err), "%v", err)
	_, _, err = countDistinctHashReference(ctx, sql.Row{long})
	require.True(t, types.ErrLengthBeyondLimit.Is(err), "%v", err)

	// The buffer's scratch space is still valid after an error.
	got, _, err := buf.hashRow(ctx, sql.Row{"a", int64(2)})
	require.NoError(t, err)
	want, _, err := countDistinctHashReference(ctx, sql.Row{"a", int64(2)})
	require.NoError(t, err)
	require.Equal(t, want, got)
}

// TestCountDistinctHashDoesNotClobberBytes checks that a []byte value, which
// ConvertToBytes returns as-is, is never used as scratch space afterwards.
func TestCountDistinctHashDoesNotClobberBytes(t *testing.T) {
	ctx := sql.NewEmptyContext()
	buf := NewCountDistinctBuffer([]sql.Expression{expression.NewStar()})
	b := make([]byte, 3, 64)
	copy(b, "abc")
	_, _, err := buf.hashRow(ctx, sql.Row{b, "zzzzzzzz"})
	require.NoError(t, err)
	require.Equal(t, "abc", string(b))
}

func TestCountDistinctBufferCountsUnchanged(t *testing.T) {
	ctx := sql.NewEmptyContext()
	rng := rand.New(rand.NewSource(42))
	exprs := []sql.Expression{
		expression.NewGetField(0, types.Text, "s", true),
		expression.NewGetField(1, types.Int64, "n", true),
		expression.NewGetField(2, types.Float64, "f", true),
	}
	buf := NewCountDistinctBuffer(exprs)
	ref := make(map[uint64]struct{})
	for i := 0; i < 2000; i++ {
		var s, n, f interface{}
		s = fmt.Sprintf("s%d", rng.Intn(20))
		n = int64(rng.Intn(10))
		f = float64(rng.Intn(5)) / 2
		switch rng.Intn(10) {
		case 0:
			s = nil
		case 1:
			n = nil
		case 2:
			f = nil
		}
		row := sql.Row{s, n, f}
		require.NoError(t, buf.Update(ctx, row))
		h, ok, err := countDistinctHashReference(ctx, row)
		require.NoError(t, err)
		if ok {
			ref[h] = struct{}{}
		}
	}
	count, err := buf.Eval(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(len(ref)), count)
	require.Equal(t, len(ref), len(buf.seen))
	for h := range ref {
		_, ok := buf.seen[h]
		require.True(t, ok)
	}
	t.Logf("2000 rows, %d distinct", len(ref))
}

// newBenchCountDistinctBuffer returns a two-expression buffer and the fixed
// row the allocation test and benchmarks feed it.
func newBenchCountDistinctBuffer() (*countDistinctBuffer, sql.Row) {
	buf := NewCountDistinctBuffer([]sql.Expression{
		expression.NewGetField(0, types.Text, "s", true),
		expression.NewGetField(1, types.Int64, "n", true),
	})
	return buf, sql.Row{"some string value", int64(1234567890)}
}

func TestCountDistinctUpdateAllocs(t *testing.T) {
	ctx := sql.NewEmptyContext()
	buf, row := newBenchCountDistinctBuffer()
	require.NoError(t, buf.Update(ctx, row))
	allocs := testing.AllocsPerRun(1000, func() {
		if err := buf.Update(ctx, row); err != nil {
			t.Fatal(err)
		}
	})
	require.Zero(t, allocs)
}

func BenchmarkCountDistinctUpdate(b *testing.B) {
	ctx := sql.NewEmptyContext()
	buf, row := newBenchCountDistinctBuffer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := buf.Update(ctx, row); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCountDistinctUpdateReference(b *testing.B) {
	ctx := sql.NewEmptyContext()
	buf, row := newBenchCountDistinctBuffer()
	seen := make(map[uint64]struct{})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		vals := make(sql.Row, len(buf.exprs))
		for j, e := range buf.exprs {
			v, err := e.Eval(ctx, row)
			if err != nil {
				b.Fatal(err)
			}
			vals[j] = v
		}
		h, _, err := countDistinctHashReference(ctx, vals)
		if err != nil {
			b.Fatal(err)
		}
		seen[h] = struct{}{}
	}
}
