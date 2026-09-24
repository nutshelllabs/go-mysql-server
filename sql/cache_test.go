// Copyright 2020-2021 Dolthub, Inc.
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

package sql

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestLRUCache(t *testing.T) {
	t.Run("basic methods", func(t *testing.T) {
		require := require.New(t)

		cache := newLRUCache(mockMemory{}, fixedReporter(5, 50), 10)

		require.NoError(cache.Put(1, "foo"))
		v, err := cache.Get(1)
		require.NoError(err)
		require.Equal("foo", v)

		_, err = cache.Get(2)
		require.Error(err)
		require.True(errors.Is(err, ErrKeyNotFound))

		// Free the cache and check previous entry disappeared.
		cache.Free()

		_, err = cache.Get(1)
		require.Error(err)
		require.True(errors.Is(err, ErrKeyNotFound))

		cache.Dispose()
		require.Panics(func() {
			_, _ = cache.Get(1)
		})
	})

	t.Run("no memory available", func(t *testing.T) {
		require := require.New(t)
		cache := newLRUCache(mockMemory{}, fixedReporter(51, 50), 5)

		require.NoError(cache.Put(1, "foo"))
		_, err := cache.Get(1)
		require.Error(err)
		require.True(errors.Is(err, ErrKeyNotFound))
	})

	t.Run("free required to add entry", func(t *testing.T) {
		require := require.New(t)
		var freed bool
		cache := newLRUCache(
			mockMemory{func() {
				freed = true
			}},
			mockReporter{func() uint64 {
				if freed {
					return 0
				}
				return 51
			}, 50},
			5,
		)
		require.NoError(cache.Put(1, "foo"))
		v, err := cache.Get(1)
		require.NoError(err)
		require.Equal("foo", v)
		require.True(freed)
	})
}

func TestHistoryCache(t *testing.T) {
	t.Run("basic methods", func(t *testing.T) {
		require := require.New(t)

		cache := newHistoryCache(mockMemory{}, fixedReporter(5, 50))

		require.NoError(cache.Put(1, "foo"))
		v, err := cache.Get(1)
		require.NoError(err)
		require.Equal("foo", v)

		_, err = cache.Get(2)
		require.Error(err)
		require.True(errors.Is(err, ErrKeyNotFound))

		cache.Dispose()
		require.Panics(func() {
			_ = cache.Put(2, "foo")
		})
	})

	t.Run("no memory available", func(t *testing.T) {
		require := require.New(t)
		cache := newHistoryCache(mockMemory{}, fixedReporter(51, 50))

		err := cache.Put(1, "foo")
		require.Error(err)
		require.True(ErrNoMemoryAvailable.Is(err))
	})

	t.Run("free required to add entry", func(t *testing.T) {
		require := require.New(t)
		var freed bool
		cache := newHistoryCache(
			mockMemory{func() {
				freed = true
			}},
			mockReporter{func() uint64 {
				if freed {
					return 0
				}
				return 51
			}, 50},
		)
		require.NoError(cache.Put(1, "foo"))
		v, err := cache.Get(1)
		require.NoError(err)
		require.Equal("foo", v)
		require.True(freed)
	})
}

func TestRowsCache(t *testing.T) {
	t.Run("basic methods", func(t *testing.T) {
		require := require.New(t)

		cache := newRowsCache(mockMemory{}, fixedReporter(5, 50))

		require.NoError(cache.Add(Row{1}))
		require.Len(cache.Get(), 1)

		cache.Dispose()
		require.Panics(func() {
			_ = cache.Add(Row{2})
		})
	})

	t.Run("no memory available", func(t *testing.T) {
		require := require.New(t)
		cache := newRowsCache(mockMemory{}, fixedReporter(51, 50))

		err := cache.Add(Row{1, "foo"})
		require.Error(err)
		require.True(ErrNoMemoryAvailable.Is(err))
	})

	t.Run("free required to add entry", func(t *testing.T) {
		require := require.New(t)
		var freed bool
		cache := newRowsCache(
			mockMemory{func() {
				freed = true
			}},
			mockReporter{func() uint64 {
				if freed {
					return 0
				}
				return 51
			}, 50},
		)
		require.NoError(cache.Add(Row{1, "foo"}))
		require.Len(cache.Get(), 1)
		require.True(freed)
	})
}

func BenchmarkHashOf(b *testing.B) {
	ctx := context.Background()
	row := NewRow(1, "1")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sum, err := HashOf(ctx, row)
		if err != nil {
			b.Fatal(err)
		}
		if sum != 11268758894040352165 {
			b.Fatalf("got %v", sum)
		}
	}
}

func BenchmarkParallelHashOf(b *testing.B) {
	ctx := context.Background()
	row := NewRow(1, "1")
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sum, err := HashOf(ctx, row)
			if err != nil {
				b.Fatal(err)
			}
			if sum != 11268758894040352165 {
				b.Fatalf("got %v", sum)
			}
		}
	})
}

// hashOfReference is a verbatim copy of the fmt-based HashOf body that the
// type-switch implementation replaced; the tests below assert the two agree.
func hashOfReference(ctx context.Context, v Row) (uint64, error) {
	hash := digestPool.Get().(*xxhash.Digest)
	hash.Reset()
	defer digestPool.Put(hash)
	for i, x := range v {
		if i > 0 {
			// separate each value in the row with a nil byte
			if _, err := hash.Write([]byte{0}); err != nil {
				return 0, err
			}
		}
		x, err := UnwrapAny(ctx, x)
		if err != nil {
			return 0, err
		}
		if _, err := fmt.Fprintf(hash, "%v,", x); err != nil {
			return 0, err
		}
	}
	return hash.Sum64(), nil
}

// hashTestStringer is a custom type with a String method, which must take the
// fmt fallback path in HashOf.
type hashTestStringer struct{ n int }

// String implements fmt.Stringer.
func (s hashTestStringer) String() string { return fmt.Sprintf("stringer<%d>", s.n) }

// hashTestJSON is a stand-in JSONWrapper (sql/types cannot be imported from
// package sql without a cycle) used to exercise the fmt fallback path.
type hashTestJSON struct{ Val interface{} }

// Clone implements JSONWrapper.
func (j hashTestJSON) Clone(context.Context) JSONWrapper { return j }

// ToInterface implements JSONWrapper.
func (j hashTestJSON) ToInterface() (interface{}, error) { return j.Val, nil }

// hashTestFloat64s are float64 values whose %v rendering has edge cases.
var hashTestFloat64s = []float64{
	math.NaN(), math.Inf(1), math.Inf(-1), math.Copysign(0, -1), 0, 1, -1, 1.5,
	1e21, 1e20, 123456789.0, 0.000001, 1e-7, 0.0001, 0.00001, 1e6, 1e-5,
	math.MaxFloat64, math.SmallestNonzeroFloat64, -math.MaxFloat64, 0.1, 1.0 / 3,
}

// hashTestFloat32s are float32 values whose %v rendering has edge cases.
var hashTestFloat32s = []float32{
	float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1)), float32(math.Copysign(0, -1)), 0,
	0.1, 1e21, 1e20, 123456789.0, 1e-7, math.MaxFloat32, math.SmallestNonzeroFloat32, 16777217,
}

func TestHashOfMatchesFmt(t *testing.T) {
	ctx := context.Background()
	loc := time.FixedZone("X", -7*3600-30*60)
	now := time.Now()
	rows := []Row{
		nil,
		{},
		{nil},
		{nil, nil, nil},
		{nil, "a", 1},
		{"a", nil, 1},
		{"a", 1, nil},
		{""},
		{"", ""},
		{"a,b,c", "x\x00y", "\x00", "héllo, 世界 🙂"},
		{int(-5), int8(-128), int16(math.MinInt16), int32(math.MaxInt32), int64(math.MinInt64), int64(math.MaxInt64)},
		{uint(7), uint8(255), uint16(math.MaxUint16), uint32(math.MaxUint32), uint64(math.MaxUint64)},
		{true, false},
		{[]byte{}, []byte{0, 1, 255}, []byte("abc")},
		{decimal.RequireFromString("-1234.5600"), decimal.New(1, 21), decimal.New(0, 0), decimal.New(-5, -30)},
		{decimal.NullDecimal{Decimal: decimal.RequireFromString("12.50"), Valid: true}, decimal.NullDecimal{}},
		{now, now.Round(0), time.Time{}, now.In(loc), time.Date(2024, 2, 29, 23, 59, 59, 123456789, time.UTC)},
		{Row{1, "a", nil, Row{2.5}}},
		{hashTestJSON{Val: map[string]any{"a": 1}}},
		{hashTestStringer{n: 3}, &hashTestStringer{n: 4}},
		{struct{ A, B int }{1, 2}, []int{1, 2}, map[string]int{"k": 1}},
		{int64(1), "1"},
		{"1", int64(1)},
	}
	for _, f := range hashTestFloat64s {
		rows = append(rows, Row{f}, Row{"x", f, int64(1)})
	}
	for _, f := range hashTestFloat32s {
		rows = append(rows, Row{f}, Row{f, nil})
	}
	for i, row := range rows {
		got, err := HashOf(ctx, row)
		require.NoError(t, err)
		want, err := hashOfReference(ctx, row)
		require.NoError(t, err)
		require.Equal(t, want, got, "row %d: %#v", i, row)
	}
}

// randomHashCell returns a random cell of one of the kinds HashOf special-cases
// (plus a few fallback kinds).
func randomHashCell(rng *rand.Rand) interface{} {
	switch rng.Intn(22) {
	case 0:
		return nil
	case 1:
		n := rng.Intn(12)
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(rng.Intn(256))
		}
		return string(b)
	case 2:
		return int(rng.Uint64())
	case 3:
		return int8(rng.Uint64())
	case 4:
		return int16(rng.Uint64())
	case 5:
		return int32(rng.Uint64())
	case 6:
		return int64(rng.Uint64())
	case 7:
		return uint(rng.Uint64())
	case 8:
		return uint8(rng.Uint64())
	case 9:
		return uint16(rng.Uint64())
	case 10:
		return uint32(rng.Uint64())
	case 11:
		return rng.Uint64()
	case 12:
		return math.Float64frombits(rng.Uint64())
	case 13:
		return math.Float32frombits(rng.Uint32())
	case 14:
		// Human-scale floats, which render in plain (non-exponent) form.
		return float64(rng.Int63n(1e9)) / math.Pow10(rng.Intn(12))
	case 15:
		return rng.Intn(2) == 0
	case 16:
		b := make([]byte, rng.Intn(6))
		rng.Read(b)
		return b
	case 17:
		loc := time.FixedZone(fmt.Sprintf("Z%d", rng.Intn(100)), (rng.Intn(48)-24)*1800)
		return time.Unix(rng.Int63n(1<<35)-(1<<34), rng.Int63n(1e9)).In(loc)
	case 18:
		return time.Now().Add(time.Duration(rng.Int63n(1e12)))
	case 19:
		return decimal.New(rng.Int63()-rng.Int63(), int32(rng.Intn(60)-30))
	case 20:
		return decimal.NullDecimal{Decimal: decimal.New(rng.Int63n(1e6), int32(rng.Intn(10)-5)), Valid: rng.Intn(2) == 0}
	default:
		return Row{rng.Int63(), math.Float64frombits(rng.Uint64())}
	}
}

func TestHashOfFuzzEquivalence(t *testing.T) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(20260924))
	const n = 300_000
	start := time.Now()
	mismatches := 0
	for i := 0; i < n; i++ {
		row := make(Row, 1+rng.Intn(5))
		for j := range row {
			row[j] = randomHashCell(rng)
		}
		got, err := HashOf(ctx, row)
		require.NoError(t, err)
		want, err := hashOfReference(ctx, row)
		require.NoError(t, err)
		if got != want {
			mismatches++
			if mismatches < 10 {
				t.Errorf("mismatch for %#v", row)
			}
		}
	}
	elapsed := time.Since(start)
	t.Logf("%d random rows, %d mismatches, %v", n, mismatches, elapsed)
	require.Zero(t, mismatches)
	require.Less(t, elapsed, 5*time.Second)
}

// hashBenchRow is a representative five-cell row for the HashOf benchmarks.
var hashBenchRow = Row{
	"some string value",
	int64(1234567890),
	3.14159,
	time.Date(2024, 5, 17, 12, 30, 45, 0, time.UTC),
	decimal.RequireFromString("-1234.5600"),
}

func BenchmarkHashOfFiveCells(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := HashOf(ctx, hashBenchRow); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHashOfFiveCellsReference(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := hashOfReference(ctx, hashBenchRow); err != nil {
			b.Fatal(err)
		}
	}
}
