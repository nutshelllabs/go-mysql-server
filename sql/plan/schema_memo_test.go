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
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// countingNode is a leaf sql.Node (pointer type) that counts its Schema() calls.
type countingNode struct {
	calls atomic.Int64
	sch   sql.Schema
}

var _ sql.Node = (*countingNode)(nil)

// newCountingNode returns a countingNode with n int64 columns named prefix0..prefix(n-1), sourced from prefix, each
// with the given default (may be nil).
func newCountingNode(prefix string, n int, def *sql.ColumnDefaultValue) *countingNode {
	sch := make(sql.Schema, n)
	for i := range sch {
		sch[i] = &sql.Column{Name: fmt.Sprintf("%s%d", prefix, i), Source: prefix, Type: types.Int64, Nullable: true, Default: def}
	}
	return &countingNode{sch: sch}
}

func (c *countingNode) Resolved() bool       { return true }
func (c *countingNode) String() string       { return "countingNode" }
func (c *countingNode) Children() []sql.Node { return nil }
func (c *countingNode) IsReadOnly() bool     { return true }

// Schema implements sql.Node and counts the call.
func (c *countingNode) Schema() sql.Schema {
	c.calls.Add(1)
	return c.sch
}

// WithChildren implements sql.Node.
func (c *countingNode) WithChildren(children ...sql.Node) (sql.Node, error) {
	if len(children) != 0 {
		return nil, sql.ErrInvalidChildrenNumber.New(c, len(children), 0)
	}
	return c, nil
}

// valueNode is a value-typed sql.Node with a non-comparable field: comparing two valueNodes through interface ==
// panics, so the memo must never compare against one.
type valueNode struct {
	names []string
}

var _ sql.Node = valueNode{}

func (v valueNode) Resolved() bool       { return true }
func (v valueNode) String() string       { return "valueNode" }
func (v valueNode) Children() []sql.Node { return nil }
func (v valueNode) IsReadOnly() bool     { return true }

// Schema implements sql.Node.
func (v valueNode) Schema() sql.Schema {
	sch := make(sql.Schema, len(v.names))
	for i, n := range v.names {
		sch[i] = &sql.Column{Name: n, Source: "value", Type: types.Int64}
	}
	return sch
}

// WithChildren implements sql.Node.
func (v valueNode) WithChildren(children ...sql.Node) (sql.Node, error) {
	return v, nil
}

const chainWidth = 8

// chainProjections returns chainWidth GetFields over a SubqueryAlias with column ids firstId..firstId+chainWidth-1;
// every other one is wrapped in an Alias.
func chainProjections(table string, firstId sql.ColumnId) []sql.Expression {
	projs := make([]sql.Expression, chainWidth)
	for j := range projs {
		gf := expression.NewGetFieldWithTable(j, 1, types.Int64, "", table, fmt.Sprintf("c%d", j), true).WithId(firstId + sql.ColumnId(j))
		if j%2 == 0 {
			projs[j] = expression.NewAlias(fmt.Sprintf("c%d", j), gf.(sql.Expression))
		} else {
			projs[j] = gf.(sql.Expression)
		}
	}
	return projs
}

// chainColSet returns the column ids firstId..firstId+chainWidth-1.
func chainColSet(firstId sql.ColumnId) sql.ColSet {
	var cols sql.ColSet
	for j := 0; j < chainWidth; j++ {
		cols.Add(firstId + sql.ColumnId(j))
	}
	return cols
}

// buildChain builds SubqueryAlias(Project(SubqueryAlias(Project(... TableAlias(leaf))))) with depth SubqueryAlias levels. Each
// SubqueryAlias carries a column set that the Project above it references, so findDefault descends into it. With
// memo false the nodes are struct literals without a memo box.
func buildChain(depth int, leaf sql.Node, memo bool) sql.Node {
	// The bottom is a TableAlias over the leaf, so that the lowest Project's findDefault reaches the leaf too.
	var base *TableAlias
	if memo {
		base = NewTableAlias("base", leaf)
	} else {
		base = &TableAlias{UnaryNode: &UnaryNode{Child: leaf}, name: "base"}
	}
	child := sql.Node(base.WithColumns(chainColSet(1)))
	projs := chainProjections("base", 1)
	for i := 0; i < depth; i++ {
		var p *Project
		if memo {
			p = NewProject(projs, child)
		} else {
			p = &Project{UnaryNode: UnaryNode{Child: child}, Projections: projs}
		}
		name := fmt.Sprintf("v%d", i)
		var sq *SubqueryAlias
		if memo {
			sq = NewSubqueryAlias(name, "", p)
		} else {
			sq = &SubqueryAlias{UnaryNode: UnaryNode{Child: p}, name: name}
		}
		firstId := sql.ColumnId((i+1)*chainWidth + 1)
		child = sq.WithColumns(chainColSet(firstId)).(*SubqueryAlias)
		projs = chainProjections(name, firstId)
	}
	return child
}

// requireSchema asserts n's schema has the given column names, all with the given source.
func requireSchema(t *testing.T, n sql.Node, source string, names ...string) {
	t.Helper()
	sch := n.Schema()
	require.Len(t, sch, len(names))
	for i, c := range sch {
		require.Equal(t, names[i], c.Name, "column %d", i)
		require.Equal(t, source, c.Source, "column %d", i)
	}
}

func TestSchemaMemoDepthChain(t *testing.T) {
	leaf := newCountingNode("c", chainWidth, nil)
	root := buildChain(8, leaf, true)

	first := root.Schema()
	require.Equal(t, int64(1), leaf.calls.Load(), "leaf Schema() must be computed once over a memoized chain")
	require.Len(t, first, chainWidth)
	require.Equal(t, cap(first), len(first))
	require.Equal(t, "v7", first[0].Source)

	second := root.Schema()
	require.Equal(t, int64(1), leaf.calls.Load())
	require.Equal(t, first, second)

	require.NoError(t, VerifySchemaMemo(root))

	// Without memo boxes the same (shallower) chain is multiplicative: this proves the chain exercises findDefault.
	unmemoLeaf := newCountingNode("c", chainWidth, nil)
	unmemo := buildChain(3, unmemoLeaf, false)
	requireSchema(t, unmemo, "v2", "c0", "c1", "c2", "c3", "c4", "c5", "c6", "c7")
	require.Greater(t, unmemoLeaf.calls.Load(), int64(chainWidth*chainWidth))
}

func TestSchemaMemoConcurrent(t *testing.T) {
	leaf := newCountingNode("c", chainWidth, nil)
	root := buildChain(8, leaf, true)
	want := buildChain(8, newCountingNode("c", chainWidth, nil), true).Schema()

	const n = 16
	results := make([]sql.Schema, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = root.Schema()
		}(i)
	}
	wg.Wait()
	for i := range results {
		require.Equal(t, want, results[i])
	}
	require.NoError(t, VerifySchemaMemo(root))
}

// defaultChild returns a SubqueryAlias over a counting leaf whose columns all carry def, with column ids 1..3, so a
// Project with GetFields 1..3 over it picks up def through findDefault.
func defaultChild(prefix string, def *sql.ColumnDefaultValue) *SubqueryAlias {
	sq := NewSubqueryAlias("t", "", newCountingNode(prefix, 3, def))
	return sq.WithColumns(sql.NewColSet(1, 2, 3)).(*SubqueryAlias)
}

// gfs returns GetFields with ids 1..len(names) named names.
func gfs(names ...string) []sql.Expression {
	out := make([]sql.Expression, len(names))
	for i, n := range names {
		out[i] = expression.NewGetFieldWithTable(i, 1, types.Int64, "", "t", n, true).WithId(sql.ColumnId(i + 1)).(sql.Expression)
	}
	return out
}

func newDefault(v int64) *sql.ColumnDefaultValue {
	return &sql.ColumnDefaultValue{Expr: expression.NewLiteral(v, types.Int64), OutType: types.Int64, Literal: true}
}

func TestSchemaMemoProjectCopies(t *testing.T) {
	defA, defB := newDefault(1), newDefault(2)

	t.Run("WithChildren", func(t *testing.T) {
		p := NewProject(gfs("x", "y"), defaultChild("a", defA))
		require.Same(t, defA, p.Schema()[0].Default)
		cp, err := p.WithChildren(defaultChild("b", defB))
		require.NoError(t, err)
		require.Same(t, defB, cp.Schema()[0].Default)
		require.Same(t, defA, p.Schema()[0].Default)
	})
	t.Run("WithExpressions", func(t *testing.T) {
		p := NewProject(gfs("x", "y"), defaultChild("a", defA))
		requireSchema(t, p, "t", "x", "y")
		cp, err := p.WithExpressions(gfs("p", "q")...)
		require.NoError(t, err)
		requireSchema(t, cp, "t", "p", "q")
		requireSchema(t, p, "t", "x", "y")
	})
	t.Run("WithCanDefer", func(t *testing.T) {
		p := NewProject(gfs("x", "y"), defaultChild("a", defA))
		require.Same(t, defA, p.Schema()[0].Default)
		cp := p.WithCanDefer(true)
		require.Same(t, defA, cp.Schema()[0].Default)
		// The copy shares the box; a key change on the copy must not leak into the original and vice versa.
		cp.Child = defaultChild("b", defB)
		require.Same(t, defB, cp.Schema()[0].Default)
		require.Same(t, defA, p.Schema()[0].Default)
		require.Same(t, defB, cp.Schema()[0].Default)
	})
	t.Run("InPlaceChild", func(t *testing.T) {
		p := NewProject(gfs("x", "y"), defaultChild("a", defA))
		require.Same(t, defA, p.Schema()[0].Default)
		p.Child = defaultChild("b", defB)
		require.Same(t, defB, p.Schema()[0].Default)
		require.NoError(t, VerifySchemaMemo(p))
	})
}

func TestSchemaMemoTableAliasCopies(t *testing.T) {
	newTA := func() *TableAlias { return NewTableAlias("ta", newCountingNode("a", 2, nil)) }

	// sharedBox checks a copy that shares the original's box: same schema, and a name change on the copy made
	// directly (bypassing WithName) is seen by the copy but not by the original.
	sharedBox := func(t *testing.T, orig, cp *TableAlias) {
		requireSchema(t, cp, "ta", "a0", "a1")
		cp.name = "renamed"
		requireSchema(t, cp, "renamed", "a0", "a1")
		requireSchema(t, orig, "ta", "a0", "a1")
		requireSchema(t, cp, "renamed", "a0", "a1")
	}
	t.Run("WithId", func(t *testing.T) {
		ta := newTA()
		requireSchema(t, ta, "ta", "a0", "a1")
		sharedBox(t, ta, ta.WithId(7).(*TableAlias))
	})
	t.Run("WithColumns", func(t *testing.T) {
		ta := newTA()
		requireSchema(t, ta, "ta", "a0", "a1")
		sharedBox(t, ta, ta.WithColumns(sql.NewColSet(1, 2)).(*TableAlias))
	})
	t.Run("WithComment", func(t *testing.T) {
		ta := newTA()
		requireSchema(t, ta, "ta", "a0", "a1")
		sharedBox(t, ta, ta.WithComment("c").(*TableAlias))
	})
	t.Run("WithName", func(t *testing.T) {
		ta := newTA()
		requireSchema(t, ta, "ta", "a0", "a1")
		cp := ta.WithName("tb")
		requireSchema(t, cp, "tb", "a0", "a1")
		requireSchema(t, ta, "ta", "a0", "a1")
	})
	t.Run("WithChildren", func(t *testing.T) {
		ta := newTA()
		requireSchema(t, ta, "ta", "a0", "a1")
		cp, err := ta.WithChildren(newCountingNode("b", 3, nil))
		require.NoError(t, err)
		requireSchema(t, cp, "ta", "b0", "b1", "b2")
		requireSchema(t, ta, "ta", "a0", "a1")
	})
}

func TestSchemaMemoSubqueryAliasCopies(t *testing.T) {
	newSQ := func() *SubqueryAlias { return NewSubqueryAlias("sq", "", newCountingNode("a", 2, nil)) }

	// sharedBox checks a copy that shares the original's box: same schema, and direct key changes on the copy
	// (child, then column names) are seen by the copy but not by the original.
	sharedBox := func(t *testing.T, orig, cp *SubqueryAlias) {
		requireSchema(t, cp, "sq", "a0", "a1")
		cp.Child = newCountingNode("b", 2, nil)
		requireSchema(t, cp, "sq", "b0", "b1")
		requireSchema(t, orig, "sq", "a0", "a1")
		cp.ColumnNames = []string{"m", "n"}
		requireSchema(t, cp, "sq", "m", "n")
		requireSchema(t, orig, "sq", "a0", "a1")
		requireSchema(t, cp, "sq", "m", "n")
	}
	t.Run("WithId", func(t *testing.T) {
		sq := newSQ()
		requireSchema(t, sq, "sq", "a0", "a1")
		sharedBox(t, sq, sq.WithId(3).(*SubqueryAlias))
	})
	t.Run("WithColumns", func(t *testing.T) {
		sq := newSQ()
		requireSchema(t, sq, "sq", "a0", "a1")
		sharedBox(t, sq, sq.WithColumns(sql.NewColSet(1, 2)).(*SubqueryAlias))
	})
	t.Run("WithCorrelated", func(t *testing.T) {
		sq := newSQ()
		requireSchema(t, sq, "sq", "a0", "a1")
		sharedBox(t, sq, sq.WithCorrelated(sql.NewColSet(5)))
	})
	t.Run("WithVolatile", func(t *testing.T) {
		sq := newSQ()
		requireSchema(t, sq, "sq", "a0", "a1")
		sharedBox(t, sq, sq.WithVolatile(true))
	})
	t.Run("WithScopeMapping", func(t *testing.T) {
		sq := newSQ()
		requireSchema(t, sq, "sq", "a0", "a1")
		sharedBox(t, sq, sq.WithScopeMapping(map[sql.ColumnId]sql.Expression{}))
	})
	t.Run("WithName", func(t *testing.T) {
		sq := newSQ()
		requireSchema(t, sq, "sq", "a0", "a1")
		requireSchema(t, sq.WithName("other"), "other", "a0", "a1")
		requireSchema(t, sq, "sq", "a0", "a1")
	})
	t.Run("WithChildren", func(t *testing.T) {
		sq := newSQ()
		requireSchema(t, sq, "sq", "a0", "a1")
		cp, err := sq.WithChildren(newCountingNode("b", 3, nil))
		require.NoError(t, err)
		requireSchema(t, cp, "sq", "b0", "b1", "b2")
		requireSchema(t, sq, "sq", "a0", "a1")
	})
	t.Run("WithChild", func(t *testing.T) {
		sq := newSQ()
		requireSchema(t, sq, "sq", "a0", "a1")
		requireSchema(t, sq.WithChild(newCountingNode("b", 1, nil)), "sq", "b0")
		requireSchema(t, sq, "sq", "a0", "a1")
	})
	t.Run("WithColumnNames", func(t *testing.T) {
		sq := newSQ()
		requireSchema(t, sq, "sq", "a0", "a1")
		requireSchema(t, sq.WithColumnNames([]string{"x", "y"}), "sq", "x", "y")
		requireSchema(t, sq, "sq", "a0", "a1")
	})
	t.Run("InPlace", func(t *testing.T) {
		sq := newSQ()
		requireSchema(t, sq, "sq", "a0", "a1")
		sq.Child = newCountingNode("b", 2, nil)
		requireSchema(t, sq, "sq", "b0", "b1")
		sq.ColumnNames = []string{"x", "y"}
		requireSchema(t, sq, "sq", "x", "y")
		sq.ColumnNames[0] = "z"
		requireSchema(t, sq, "sq", "z", "y")
		sq.ColumnNames = nil
		requireSchema(t, sq, "sq", "b0", "b1")
		require.NoError(t, VerifySchemaMemo(sq))
	})
}

func TestSchemaMemoPrependRowInPlan(t *testing.T) {
	inner := NewProject(gfs("x", "y"), newCountingNode("a", 2, nil))
	sq := NewSubqueryAlias("sq", "", inner)
	sq.OuterScopeVisibility = true
	orig := sq.Schema()
	requireSchema(t, sq, "sq", "x", "y")

	n, _, err := PrependRowInPlan(sql.Row{int64(1)}, false)(sq)
	require.NoError(t, err)
	cp := n.(*SubqueryAlias)
	require.NotSame(t, sq, cp)
	require.NotSame(t, sq.schemaMemo, cp.schemaMemo)
	requireSchema(t, cp, "sq", "x", "y")
	require.NoError(t, VerifySchemaMemo(cp))

	// The original keeps its own memo entry.
	require.Equal(t, orig, sq.Schema())
	require.Same(t, &orig[0], &sq.Schema()[0])
}

func TestSchemaMemoZeroValueAndValueChild(t *testing.T) {
	t.Run("NilBox", func(t *testing.T) {
		p := &Project{UnaryNode: UnaryNode{Child: newCountingNode("a", 2, nil)}, Projections: gfs("x", "y")}
		requireSchema(t, p, "t", "x", "y")
		requireSchema(t, p, "t", "x", "y")
		sq := &SubqueryAlias{UnaryNode: UnaryNode{Child: p}, name: "sq"}
		requireSchema(t, sq, "sq", "x", "y")
		cp := sq.WithId(2).(*SubqueryAlias)
		requireSchema(t, cp, "sq", "x", "y")
		ta := &TableAlias{UnaryNode: &UnaryNode{Child: p}, name: "ta"}
		requireSchema(t, ta, "ta", "x", "y")
		require.NoError(t, VerifySchemaMemo(sq))
		require.NoError(t, VerifySchemaMemo(ta))
	})
	t.Run("ValueChild", func(t *testing.T) {
		v := valueNode{names: []string{"x", "y"}}
		p := NewProject(gfs("x", "y"), v)
		requireSchema(t, p, "t", "x", "y")
		requireSchema(t, p, "t", "x", "y")
		ta := NewTableAlias("ta", v)
		requireSchema(t, ta, "ta", "x", "y")
		requireSchema(t, ta, "ta", "x", "y")
		sq := NewSubqueryAlias("sq", "", v)
		requireSchema(t, sq, "sq", "x", "y")
		requireSchema(t, sq, "sq", "x", "y")
		// A memo stored over a pointer child, then a value child swapped in place: the key check must not panic.
		ta2 := NewTableAlias("ta", newCountingNode("a", 1, nil))
		requireSchema(t, ta2, "ta", "a0")
		ta2.Child = v
		requireSchema(t, ta2, "ta", "x", "y")
		ta2.Child = valueNode{names: []string{"z"}}
		requireSchema(t, ta2, "ta", "z")
		require.NoError(t, VerifySchemaMemo(ta2))
	})
}

func TestSchemaMemoSubqueryType(t *testing.T) {
	one := NewSubquery(NewProject(gfs("x"), newCountingNode("a", 1, nil)), "")
	require.Equal(t, types.Int64, one.Type())

	two := NewSubquery(NewProject(gfs("x", "y"), newCountingNode("a", 2, nil)), "")
	require.Equal(t, types.CreateTuple(types.Int64, types.Int64), two.Type())
	_, ok := two.Type().(types.TupleType)
	require.True(t, ok)
}

func TestSchemaMemoVerify(t *testing.T) {
	root := buildChain(8, newCountingNode("c", chainWidth, nil), true)
	require.NoError(t, VerifySchemaMemo(root))

	// Poison a SubqueryAlias memo nested inside a subquery expression: matching keys, wrong schema.
	sq := NewSubqueryAlias("sq", "", newCountingNode("a", 2, nil))
	good := sq.Schema()
	bad := make(sql.Schema, len(good))
	for i, c := range good {
		cc := *c
		cc.Name = "wrong"
		bad[i] = &cc
	}
	sq.schemaMemo.store(&nodeSchema{child: sq.Child, name: sq.name, schema: bad})
	outer := NewProject([]sql.Expression{NewSubquery(sq, "select ...")}, newCountingNode("o", 1, nil))
	err := VerifySchemaMemo(outer)
	require.Error(t, err)
	require.Contains(t, err.Error(), "column 0")
	require.Contains(t, err.Error(), "wrong")

	// A memoized schema with spare capacity is reported too.
	ta := NewTableAlias("ta", newCountingNode("a", 2, nil))
	withCap := make(sql.Schema, 2, 4)
	copy(withCap, ta.Schema())
	ta.schemaMemo.store(&nodeSchema{child: ta.Child, name: ta.name, schema: withCap})
	err = VerifySchemaMemo(ta)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cap 4 != len 2")
}
