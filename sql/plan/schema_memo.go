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
	"reflect"
	"sync/atomic"

	"github.com/dolthub/go-mysql-server/sql"
)

// nodeSchema is one memoized Schema() result of a Project, TableAlias or SubqueryAlias, together with the inputs
// that result was computed from. A memo is only valid for a receiver whose inputs still match the stored keys.
// A nodeSchema is immutable once it has been published into a schemaMemo.
type nodeSchema struct {
	// child is the node's child at computation time. Always a pointer-kind dynamic type (see memoizableChild).
	child sql.Node
	// projs is the Project's projection slice at computation time, compared by identity (Project only).
	projs []sql.Expression
	// name is the alias name at computation time (TableAlias and SubqueryAlias only).
	name string
	// colNames is the SubqueryAlias ColumnNames at computation time, compared by content (SubqueryAlias only).
	colNames []string
	// schema is the computed schema. Its cap equals its len, so appending to it never writes into the memo.
	schema sql.Schema
}

// schemaMemo is a heap box holding the latest published nodeSchema of a node. Nodes hold a *schemaMemo rather than
// an inline atomic.Pointer so that the ubiquitous `np := *n` copies in With* methods copy only the box pointer (an
// inline atomic would be copied non-atomically and is flagged by go vet). Copies that share a box stay correct
// because every lookup checks the keys against the receiver; the box is only a place to publish results.
type schemaMemo struct {
	p atomic.Pointer[nodeSchema]
}

// newSchemaMemo returns an empty memo box.
func newSchemaMemo() *schemaMemo {
	return &schemaMemo{}
}

// load returns the published nodeSchema, or nil when the box is nil or empty.
func (m *schemaMemo) load() *nodeSchema {
	if m == nil {
		return nil
	}
	return m.p.Load()
}

// store publishes ns unless the box is nil (a node built without its constructor), in which case nothing is cached.
func (m *schemaMemo) store(ns *nodeSchema) {
	if m == nil {
		return
	}
	m.p.Store(ns)
}

// memoizableChild reports whether a schema computed over child may be memoized. The memo key compares the stored
// child with the receiver's child using interface ==, which panics when both operands have the same non-comparable
// dynamic type (some sql.Node implementations are value types with slice or map fields). Only memoizing over
// pointer-kind children guarantees the comparison never panics: a stored pointer compares by address, and a
// receiver child of any other dynamic type simply compares unequal.
func memoizableChild(child sql.Node) bool {
	if child == nil {
		return false
	}
	return reflect.TypeOf(child).Kind() == reflect.Pointer
}

// sameExprSlice reports whether a and b are the same slice (same length and same backing array start).
func sameExprSlice(a, b []sql.Expression) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// sameStrings reports whether a and b have identical contents.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// cloneStrings returns a copy of s, so that later in-place edits of s are detected by the memo key check.
func cloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// VerifySchemaMemo walks the whole plan rooted at n, including the Query of every *Subquery expression, and checks
// every *Project, *TableAlias and *SubqueryAlias: its memoized Schema() must be stable across two calls, must have
// cap == len, and must equal an uncached recomputation. It returns a descriptive error for the first mismatch. It is
// intended for tests.
func VerifySchemaMemo(n sql.Node) error {
	var err error
	walkPlanForSchemaMemo(n, func(node sql.Node) bool {
		err = verifyNodeSchemaMemo(node)
		return err == nil
	})
	return err
}

// walkPlanForSchemaMemo calls f on n and every node below it, descending into subquery expressions. It stops as soon
// as f returns false and reports whether the walk ran to completion.
func walkPlanForSchemaMemo(n sql.Node, f func(sql.Node) bool) bool {
	if n == nil {
		return true
	}
	if !f(n) {
		return false
	}
	if ex, ok := n.(sql.Expressioner); ok {
		cont := true
		for _, e := range ex.Expressions() {
			sql.Inspect(e, func(e sql.Expression) bool {
				if !cont {
					return false
				}
				if sq, ok := e.(*Subquery); ok && sq.Query != nil {
					cont = walkPlanForSchemaMemo(sq.Query, f)
				}
				return cont
			})
			if !cont {
				return false
			}
		}
	}
	for _, c := range n.Children() {
		if !walkPlanForSchemaMemo(c, f) {
			return false
		}
	}
	return true
}

// verifyNodeSchemaMemo checks one node's memoized schema against an uncached recomputation.
func verifyNodeSchemaMemo(n sql.Node) error {
	var compute func() sql.Schema
	switch n := n.(type) {
	case *Project:
		compute = n.computeSchema
	case *TableAlias:
		compute = n.computeSchema
	case *SubqueryAlias:
		compute = n.computeSchema
	default:
		return nil
	}
	first := n.Schema()
	second := n.Schema()
	fresh := compute()
	if cap(first) != len(first) {
		return fmt.Errorf("schema memo: cap %d != len %d for node %s", cap(first), len(first), sql.DebugString(n))
	}
	if err := compareSchemas(n, "first Schema() call", first, "second Schema() call", second); err != nil {
		return err
	}
	return compareSchemas(n, "memoized schema", first, "recomputed schema", fresh)
}

// compareSchemas compares a and b column by column using (*sql.Column).Equals and reflect.DeepEqual and returns an
// error naming node, the column index and both columns on the first mismatch.
func compareSchemas(n sql.Node, aName string, a sql.Schema, bName string, b sql.Schema) error {
	if len(a) != len(b) {
		return fmt.Errorf("schema memo: %s has %d columns but %s has %d for node %s", aName, len(a), bName, len(b), sql.DebugString(n))
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if (ca == nil) != (cb == nil) || (ca != nil && (!ca.Equals(cb) || !reflect.DeepEqual(ca, cb))) {
			return fmt.Errorf("schema memo: column %d differs for node %s: %s=%#v, %s=%#v", i, sql.DebugString(n), aName, ca, bName, cb)
		}
	}
	return nil
}
