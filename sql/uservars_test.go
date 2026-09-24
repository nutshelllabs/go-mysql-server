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

package sql

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGetUserVariableConcurrentReads exercises concurrent GetUserVariable
// calls against a concurrent writer; run with -race to check the locking.
func TestGetUserVariableConcurrentReads(t *testing.T) {
	ctx := NewEmptyContext()
	uv := NewUserVars()
	require.NoError(t, uv.SetUserVariable(ctx, "Shared", int64(42), nil))

	const readers = 16
	const iterations = 1000
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_, val, err := uv.GetUserVariable(ctx, "shared")
				if err != nil {
					errs <- err
					return
				}
				if val != int64(42) {
					errs <- fmt.Errorf("got %v, want 42", val)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = uv.SetUserVariable(ctx, fmt.Sprintf("other%d", i%8), int64(i), nil)
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	_, val, err := uv.GetUserVariable(ctx, "other7")
	require.NoError(t, err)
	require.NotNil(t, val)
}
