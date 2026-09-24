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

package rowexec

import (
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/plan"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// errHashLookupTestDrain is the error a countingSource iterator returns when it is told to fail.
var errHashLookupTestDrain = errors.New("hash lookup test: drain failed")

// countingSource is a leaf sql.ExecSourceRel that serves a fixed row set and records how often it was built and
// how often its iterators were closed.
type countingSource struct {
	rows []sql.Row
	// failAt, when positive, makes the iterator's failAt-th Next call return errHashLookupTestDrain.
	failAt int
	// builds counts RowIter calls.
	builds int
	// closes counts Close calls across every iterator this source returned.
	closes int
}

var _ sql.ExecSourceRel = (*countingSource)(nil)

// Resolved implements sql.Node.
func (s *countingSource) Resolved() bool { return true }

// String implements sql.Node.
func (s *countingSource) String() string { return "countingSource" }

// Schema implements sql.Node.
func (s *countingSource) Schema() sql.Schema {
	return sql.Schema{
		{Name: "k", Type: types.Int64, Nullable: true},
		{Name: "v", Type: types.Int64, Nullable: true},
	}
}

// Children implements sql.Node.
func (s *countingSource) Children() []sql.Node { return nil }

// WithChildren implements sql.Node.
func (s *countingSource) WithChildren(children ...sql.Node) (sql.Node, error) {
	if len(children) != 0 {
		return nil, sql.ErrInvalidChildrenNumber.New(s, len(children), 0)
	}
	return s, nil
}

// IsReadOnly implements sql.Node.
func (s *countingSource) IsReadOnly() bool { return true }

// RowIter implements sql.ExecSourceRel.
func (s *countingSource) RowIter(ctx *sql.Context, r sql.Row) (sql.RowIter, error) {
	s.builds++
	return &countingSourceIter{src: s}, nil
}

// countingSourceIter iterates a countingSource's rows and reports its Close calls to the source.
type countingSourceIter struct {
	src   *countingSource
	pos   int
	nexts int
}

// Next implements sql.RowIter.
func (i *countingSourceIter) Next(ctx *sql.Context) (sql.Row, error) {
	i.nexts++
	if i.src.failAt > 0 && i.nexts == i.src.failAt {
		return nil, errHashLookupTestDrain
	}
	if i.pos >= len(i.src.rows) {
		return nil, io.EOF
	}
	r := i.src.rows[i.pos]
	i.pos++
	return r, nil
}

// Close implements sql.RowIter.
func (i *countingSourceIter) Close(ctx *sql.Context) error {
	i.src.closes++
	return nil
}

// newHashLookupTestNode returns a HashLookup over src keyed on the build row's first column and the probe row's
// first column.
func newHashLookupTestNode(src *countingSource, jt plan.JoinType) *plan.HashLookup {
	rightKey := expression.NewGetField(0, types.Int64, "k", true)
	leftKey := expression.NewGetField(0, types.Int64, "p", true)
	return plan.NewHashLookup(src, rightKey, leftKey, jt)
}

// newHashLookupTestSource returns a countingSource with keys 1,2,2,3,4.
func newHashLookupTestSource() *countingSource {
	return &countingSource{rows: []sql.Row{
		{int64(1), int64(10)},
		{int64(2), int64(20)},
		{int64(2), int64(21)},
		{int64(3), int64(30)},
		{int64(4), int64(40)},
	}}
}

// probeHashLookup builds hl for probe and drains the result.
func probeHashLookup(t *testing.T, ctx *sql.Context, hl *plan.HashLookup, probe sql.Row) []sql.Row {
	t.Helper()
	iter, err := DefaultBuilder.Build(ctx, hl, probe)
	require.NoError(t, err)
	rows, err := sql.RowIterToRows(ctx, iter)
	require.NoError(t, err)
	return rows
}

// newHashLookupTestContext returns a context over an empty memory database.
func newHashLookupTestContext() *sql.Context {
	return newContext(memory.NewDBProvider(memory.NewDatabase("test")))
}

func TestHashLookupFirstProbeAnswersBucket(t *testing.T) {
	ctx := newHashLookupTestContext()
	src := newHashLookupTestSource()
	hl := newHashLookupTestNode(src, plan.JoinTypeInner)

	rows := probeHashLookup(t, ctx, hl, sql.Row{int64(2)})
	require.Equal(t, []sql.Row{{int64(2), int64(20)}, {int64(2), int64(21)}}, rows)
	require.NotNil(t, hl.Lookup)
	require.Equal(t, 1, src.builds)
	require.Equal(t, 1, src.closes)
}

func TestHashLookupBuildsChildOnce(t *testing.T) {
	ctx := newHashLookupTestContext()
	src := newHashLookupTestSource()
	hl := newHashLookupTestNode(src, plan.JoinTypeInner)

	require.Len(t, probeHashLookup(t, ctx, hl, sql.Row{int64(2)}), 2)
	require.Equal(t, []sql.Row{{int64(4), int64(40)}}, probeHashLookup(t, ctx, hl, sql.Row{int64(4)}))
	require.Empty(t, probeHashLookup(t, ctx, hl, sql.Row{int64(9)}))
	require.Equal(t, 1, src.builds)
	require.Equal(t, 1, src.closes)
}

func TestHashLookupDrainErrorLeavesLookupNil(t *testing.T) {
	ctx := newHashLookupTestContext()
	src := newHashLookupTestSource()
	src.failAt = 3
	hl := newHashLookupTestNode(src, plan.JoinTypeInner)

	iter, err := DefaultBuilder.Build(ctx, hl, sql.Row{int64(2)})
	if err == nil {
		// Surface an error that only shows up while iterating, so the assertions below report it.
		_, err = sql.RowIterToRows(ctx, iter)
	}
	require.ErrorIs(t, err, errHashLookupTestDrain)
	require.Nil(t, hl.Lookup)
	require.Equal(t, 1, src.closes)
}

func TestHashLookupNullKeys(t *testing.T) {
	ctx := newHashLookupTestContext()
	src := newHashLookupTestSource()
	src.rows = append(src.rows, sql.Row{nil, int64(99)})
	hl := newHashLookupTestNode(src, plan.JoinTypeInner)

	require.Equal(t, []sql.Row{{nil, int64(99)}}, probeHashLookup(t, ctx, hl, sql.Row{nil}))
	require.Equal(t, []sql.Row{{nil, int64(99)}}, probeHashLookup(t, ctx, hl, sql.Row{nil}))
	require.Equal(t, 1, src.builds)
}

func TestHashLookupExcludeNullsKeepsPassThrough(t *testing.T) {
	ctx := newHashLookupTestContext()
	src := newHashLookupTestSource()
	hl := newHashLookupTestNode(src, plan.JoinTypeLeftOuterHashExcludeNulls)

	iter, err := DefaultBuilder.Build(ctx, hl, sql.Row{int64(9)})
	require.NoError(t, err)
	require.Nil(t, hl.Lookup, "the pass-through iterator publishes the map only at EOF")
	rows, err := sql.RowIterToRows(ctx, iter)
	require.NoError(t, err)
	require.Equal(t, src.rows, rows, "the first missed probe walks the whole build side in build order")
	require.NotNil(t, hl.Lookup)

	require.NotEmpty(t, probeHashLookup(t, ctx, hl, sql.Row{int64(9)}))
	require.Equal(t, 1, src.builds)
}
