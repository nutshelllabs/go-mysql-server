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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/expression/function"
	"github.com/dolthub/go-mysql-server/sql/plan"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// dateTableDays are the days stored in the test table built by newDateTable; 2026-09-13 is deliberately absent.
var dateTableDays = []int{11, 12, 14, 15}

// newDateTable returns a memory table `d` with columns `t TEXT` and `dt DATE` holding the same dates as text and as
// DATE. When withNull is true it also holds a row whose columns are both NULL.
func newDateTable(t *testing.T, ctx *sql.Context, db *memory.Database, withNull bool) *memory.Table {
	table := memory.NewTable(db.BaseDatabase, "d", sql.NewPrimaryKeySchema(sql.Schema{
		{Name: "t", Source: "d", Type: types.Text, Nullable: true},
		{Name: "dt", Source: "d", Type: types.Date, Nullable: true},
	}), nil)
	for _, day := range dateTableDays {
		tm := time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC)
		require.NoError(t, table.Insert(ctx, sql.Row{tm.Format("2006-01-02"), tm}))
	}
	if withNull {
		require.NoError(t, table.Insert(ctx, sql.Row{nil, nil}))
	}
	return table
}

func TestInSubqueryHashesBothSidesThroughOneType(t *testing.T) {
	db := memory.NewDatabase("foo")
	pro := memory.NewDBProvider(db)
	ctx := newContext(pro)

	// The outer row is (probe TEXT); inside the subquery the scope row is prepended, so t is at 1 and dt at 2.
	probe := expression.NewGetField(0, types.Text, "probe", true)
	colT := expression.NewGetField(1, types.Text, "t", true)
	colDt := expression.NewGetField(2, types.Date, "dt", true)

	shapes := []struct {
		name  string
		left  sql.Expression
		right sql.Expression
	}{
		{"text IN (SELECT dt)", probe, colDt},
		{"CAST(text AS DATE) IN (SELECT t)", expression.NewConvert(probe, expression.ConvertToDate), colT},
		{"text IN (SELECT DATE(t))", probe, function.NewDate(colT)},
		{"DATE(text) IN (SELECT t)", function.NewDate(probe), colT},
	}
	cases := []struct {
		name     string
		probe    interface{}
		withNull bool
		want     interface{}
	}{
		{"present", "2026-09-14", false, true},
		{"absent", "2026-09-13", false, false},
		{"present with NULL build row", "2026-09-14", true, true},
		{"absent with NULL build row", "2026-09-13", true, nil},
	}

	for _, withNull := range []bool{false, true} {
		table := newDateTable(t, ctx, db, withNull)
		for _, shape := range shapes {
			for _, tc := range cases {
				if tc.withNull != withNull {
					continue
				}
				t.Run(shape.name+"/"+tc.name, func(t *testing.T) {
					node := plan.NewProject([]sql.Expression{shape.right}, plan.NewResolvedTable(table, nil, nil))
					result, err := plan.NewInSubquery(
						shape.left,
						plan.NewSubquery(node, "").WithExecBuilder(DefaultBuilder),
					).Eval(ctx, sql.NewRow(tc.probe))
					require.NoError(t, err)
					require.Equal(t, tc.want, result)
				})
			}
		}
	}
}

func TestInSubqueryTupleKeysUnchanged(t *testing.T) {
	db := memory.NewDatabase("foo")
	pro := memory.NewDBProvider(db)
	ctx := newContext(pro)
	table := newDateTable(t, ctx, db, false)

	node := plan.NewProject([]sql.Expression{
		expression.NewGetField(0, types.Text, "t", true),
		expression.NewGetField(1, types.Date, "dt", true),
	}, plan.NewResolvedTable(table, nil, nil))

	cases := []struct {
		name string
		text string
		day  int
		want interface{}
	}{
		{"present", "2026-09-14", 14, true},
		{"absent", "2026-09-13", 13, false},
		{"mismatched pair", "2026-09-14", 15, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			left := expression.NewTuple(
				expression.NewLiteral(tc.text, types.Text),
				expression.NewLiteral(time.Date(2026, 9, tc.day, 0, 0, 0, 0, time.UTC), types.Date),
			)
			result, err := plan.NewInSubquery(
				left,
				plan.NewSubquery(node, "").WithExecBuilder(DefaultBuilder),
			).Eval(ctx, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, result)
		})
	}
}

func TestSubqueryHashCacheRekeysOnTypeChange(t *testing.T) {
	db := memory.NewDatabase("foo")
	pro := memory.NewDBProvider(db)
	ctx := newContext(pro)
	table := newDateTable(t, ctx, db, false)

	node := plan.NewProject([]sql.Expression{
		expression.NewGetField(0, types.Text, "t", true),
	}, plan.NewResolvedTable(table, nil, nil))
	sq := plan.NewSubquery(node, "").WithExecBuilder(DefaultBuilder)
	defer sq.Dispose()

	rawKey, err := sql.HashOf(ctx, sql.NewRow("2026-09-14"))
	require.NoError(t, err)
	tm := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	convertedKey, err := sql.HashOf(ctx, sql.NewRow(tm))
	require.NoError(t, err)

	raw, err := sq.HashMultipleWithType(ctx, nil, nil)
	require.NoError(t, err)
	_, err = raw.Get(rawKey)
	require.NoError(t, err, "raw hashing finds the text key")

	converted, err := sq.HashMultipleWithType(ctx, nil, types.DatetimeMaxPrecision)
	require.NoError(t, err)
	val, err := converted.Get(convertedKey)
	require.NoError(t, err, "the rebuilt cache finds the converted key")
	require.Equal(t, tm, val)
	_, err = converted.Get(rawKey)
	require.Error(t, err, "the rebuilt cache no longer holds the raw key")

	again, err := sq.HashMultipleWithType(ctx, nil, nil)
	require.NoError(t, err)
	_, err = again.Get(rawKey)
	require.NoError(t, err, "switching back to raw hashing rebuilds again")
}
