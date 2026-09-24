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

package analyzer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression/function"
	"github.com/dolthub/go-mysql-server/sql/plan"
	"github.com/dolthub/go-mysql-server/sql/planbuilder"
	"github.com/dolthub/go-mysql-server/sql/transform"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// TestPruneTablesThroughHaving checks that pruneTables walks through a
// *plan.Having node, pruning the table scan below it to the columns the
// query actually references, and that every column referenced by the HAVING
// condition survives the pruning.
func TestPruneTablesThroughHaving(t *testing.T) {
	tests := []struct {
		name string
		q    string
		exp  []string
	}{
		{
			name: "having over group by keeps aggregated column",
			q:    "SELECT x FROM xy GROUP BY x HAVING SUM(y) > 0",
			exp:  []string{"x", "y"},
		},
		{
			name: "having without group by keeps referenced column",
			q:    "SELECT COUNT(x) FROM xy HAVING SUM(y) > 0",
			exp:  []string{"x", "y"},
		},
		{
			name: "having without aggregation references a select alias",
			// The unanalyzed planbuilder output projects every table column
			// below the Having (the alias projection), so nothing can be
			// dropped, but the walk must still reach the scan.
			q:   "SELECT x AS k, y FROM xy HAVING k > 0",
			exp: []string{"x", "y", "z"},
		},
		{
			name: "having references only the grouping column",
			q:    "SELECT x FROM xy GROUP BY x HAVING x > 0",
			exp:  []string{"x"},
		},
	}

	db := memory.NewDatabase("mydb")
	cat := &sql.MapCatalog{
		Databases: map[string]sql.Database{"mydb": db},
		Tables:    make(map[string]sql.Table),
		Funcs:     function.NewRegistry(),
	}
	xy := memory.NewTable(db, "xy", sql.NewPrimaryKeySchema(sql.Schema{
		{Name: "x", Type: types.Int64, Source: "xy"},
		{Name: "y", Type: types.Int64, Source: "xy"},
		{Name: "z", Type: types.Int64, Source: "xy"},
	}, 0), nil)
	cat.Tables["xy"] = xy
	db.AddTable("xy", xy)
	pro := memory.NewDBProvider(db)
	sess := memory.NewSession(sql.NewBaseSession(), pro)
	ctx := sql.NewContext(context.Background(), sql.WithSession(sess))
	ctx.SetCurrentDatabase("mydb")
	b := planbuilder.New(ctx, cat, nil, nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b.Reset()
			node, _, _, _, err := b.Parse(tt.q, nil, false)
			require.NoError(t, err)
			require.True(t, hasHaving(node), "expected a Having node in:\n%s", sql.DebugString(node))

			pruned, _, err := pruneTables(ctx, nil, node, nil, nil, nil)
			require.NoError(t, err)
			t.Logf("pruned plan:\n%s", sql.DebugString(pruned))

			require.Equal(t, tt.exp, scanProjections(pruned))
		})
	}
}

func hasHaving(n sql.Node) bool {
	return transform.InspectUp(n, func(n sql.Node) bool {
		_, ok := n.(*plan.Having)
		return ok
	})
}

// scanProjections returns the projected column names of the single
// *plan.ResolvedTable in |n|, or nil if the scan is unpruned.
func scanProjections(n sql.Node) []string {
	var cols []string
	transform.Inspect(n, func(n sql.Node) bool {
		if rt, ok := n.(*plan.ResolvedTable); ok {
			if pt, ok := rt.Table.(sql.ProjectedTable); ok {
				cols = pt.Projections()
			}
			return false
		}
		return true
	})
	return cols
}
