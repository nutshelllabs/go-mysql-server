// Copyright 2025 Dolthub, Inc.
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

package plan

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// newTupleHashLookup returns an inner-join HashLookup whose entry and probe keys are both the two-column tuple
// (int64 idx 0, text idx 1), over a leaf child with a matching schema.
func newTupleHashLookup() (*HashLookup, expression.Tuple) {
	child := &countingNode{sch: sql.Schema{
		{Name: "a", Source: "t", Type: types.Int64, Nullable: true},
		{Name: "b", Source: "t", Type: types.Text, Nullable: true},
	}}
	key := expression.NewTuple(
		expression.NewGetField(0, types.Int64, "a", true),
		expression.NewGetField(1, types.Text, "b", true),
	)
	return NewHashLookup(child, key, key, JoinTypeInner), key
}

// TestHashLookupGetHashKeyCachedType checks that the cached probe-key type yields the same hash key as calling
// LeftProbeKey.Type() per row, and that a NULL-typed probe key still maps to a nil key.
func TestHashLookupGetHashKeyCachedType(t *testing.T) {
	ctx := sql.NewEmptyContext()
	n, key := newTupleHashLookup()
	require.NotNil(t, n.probeKeyType)
	row := sql.Row{int64(42), "hello"}

	got, err := n.GetHashKey(ctx, n.LeftProbeKey, row)
	require.NoError(t, err)

	uncached := *n
	uncached.probeKeyType = nil
	want, err := uncached.GetHashKey(ctx, uncached.LeftProbeKey, row)
	require.NoError(t, err)
	require.Equal(t, want, got)

	v, err := key.Eval(ctx, row)
	require.NoError(t, err)
	v, _, err = key.Type().Convert(ctx, v)
	require.NoError(t, err)
	ref, err := sql.HashOf(ctx, v.([]interface{}))
	require.NoError(t, err)
	require.Equal(t, ref, got)

	nullKey := expression.NewLiteral(nil, types.Null)
	nn := NewHashLookup(n.Child, nullKey, nullKey, JoinTypeInner)
	got, err = nn.GetHashKey(ctx, nn.LeftProbeKey, row)
	require.NoError(t, err)
	require.Nil(t, got)
}

// hashKeySink forces the reference closure in TestHashLookupGetHashKeyAllocs to box its hash like GetHashKey does.
var hashKeySink interface{}

// TestHashLookupGetHashKeyAllocs checks that GetHashKey allocates no more than Eval + Convert + HashOf with the
// cached type, and strictly less than when the type is recomputed per row.
func TestHashLookupGetHashKeyAllocs(t *testing.T) {
	ctx := sql.NewEmptyContext()
	n, key := newTupleHashLookup()
	row := sql.Row{int64(42), "hello"}

	require.Greater(t, testing.AllocsPerRun(2000, func() { _ = key.Type() }), 0.0)

	after := testing.AllocsPerRun(2000, func() { _, _ = n.GetHashKey(ctx, n.LeftProbeKey, row) })
	ref := testing.AllocsPerRun(2000, func() {
		v, _ := key.Eval(ctx, row)
		v, _, _ = n.probeKeyType.Convert(ctx, v)
		h, _ := sql.HashOf(ctx, v.([]interface{}))
		hashKeySink = h // GetHashKey returns the hash boxed in an interface{}
	})
	require.Equal(t, ref, after)

	cached := n.probeKeyType
	n.probeKeyType = nil
	before := testing.AllocsPerRun(2000, func() { _, _ = n.GetHashKey(ctx, n.LeftProbeKey, row) })
	n.probeKeyType = cached
	t.Logf("GetHashKey allocs/op: before=%v after=%v reference=%v", before, after, ref)
	require.Greater(t, before, after)
}

// TestHashLookupWithExpressionsRecomputesType checks that WithExpressions replaces the cached probe-key type, so a
// new key's type is used for conversion rather than the old one.
func TestHashLookupWithExpressionsRecomputesType(t *testing.T) {
	ctx := sql.NewEmptyContext()
	n, _ := newTupleHashLookup()
	probe := expression.NewGetField(0, types.Int64, "a", true)
	nn, err := n.WithExpressions(probe, probe)
	require.NoError(t, err)
	hl := nn.(*HashLookup)
	require.Equal(t, types.Int64, hl.probeKeyType)

	got, err := hl.GetHashKey(ctx, expression.NewLiteral("7", types.Text), nil)
	require.NoError(t, err)
	require.Equal(t, int64(7), got)
}
