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
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/shopspring/decimal"

	lru "github.com/hashicorp/golang-lru"
)

// HashOf returns a hash of the given value to be used as key in a cache.
//
// Each cell is hashed as the bytes fmt's "%v," would print for it, with cells
// separated by a 0 byte. Common scalar types are formatted directly into a
// stack buffer (byte-for-byte what fmt would produce); every other type goes
// through fmt itself, so the hash values are unchanged from the fmt-only form.
func HashOf(ctx context.Context, v Row) (uint64, error) {
	hash := digestPool.Get().(*xxhash.Digest)
	hash.Reset()
	defer digestPool.Put(hash)
	var scratch [64]byte
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
		// TODO: we don't have the type info necessary to appropriately encode the value of a string with a non-standard
		//  collation, which means that two strings that differ only in their collations will hash to the same value.
		//  See rowexec/grouping_key()
		b, ok := appendHashCell(scratch[:0], x)
		if !ok {
			if _, err := fmt.Fprintf(hash, "%v,", x); err != nil {
				return 0, err
			}
			continue
		}
		if _, err := hash.Write(append(b, ',')); err != nil {
			return 0, err
		}
	}
	return hash.Sum64(), nil
}

// hashTimeLayout is the layout time.Time.String uses before any monotonic
// clock suffix.
const hashTimeLayout = "2006-01-02 15:04:05.999999999 -0700 MST"

// appendHashCell appends to b exactly the bytes fmt's "%v" verb prints for x,
// for the scalar types it handles directly. It reports false (and HashOf falls
// back to fmt) for every other type.
func appendHashCell(b []byte, x interface{}) ([]byte, bool) {
	switch x := x.(type) {
	case nil:
		return append(b, "<nil>"...), true
	case string:
		return append(b, x...), true
	case int:
		return strconv.AppendInt(b, int64(x), 10), true
	case int8:
		return strconv.AppendInt(b, int64(x), 10), true
	case int16:
		return strconv.AppendInt(b, int64(x), 10), true
	case int32:
		return strconv.AppendInt(b, int64(x), 10), true
	case int64:
		return strconv.AppendInt(b, x, 10), true
	case uint:
		return strconv.AppendUint(b, uint64(x), 10), true
	case uint8:
		return strconv.AppendUint(b, uint64(x), 10), true
	case uint16:
		return strconv.AppendUint(b, uint64(x), 10), true
	case uint32:
		return strconv.AppendUint(b, uint64(x), 10), true
	case uint64:
		return strconv.AppendUint(b, x, 10), true
	case float64:
		return strconv.AppendFloat(b, x, 'g', -1, 64), true
	case float32:
		return strconv.AppendFloat(b, float64(x), 'g', -1, 32), true
	case bool:
		return strconv.AppendBool(b, x), true
	case time.Time:
		// Round(0) strips a monotonic clock reading; String appends one as
		// " m=..." when present, so only the reading-free case is formatted
		// here and the other goes through String itself.
		if x == x.Round(0) {
			return x.AppendFormat(b, hashTimeLayout), true
		}
		return append(b, x.String()...), true
	case decimal.Decimal:
		return append(b, x.String()...), true
	default:
		return b, false
	}
}

var digestPool = sync.Pool{
	New: func() any {
		return xxhash.New()
	},
}

// ErrKeyNotFound is returned when the key could not be found in the cache.
var ErrKeyNotFound = fmt.Errorf("memory: key not found in cache")

type lruCache struct {
	memory   Freeable
	reporter Reporter
	size     int
	cache    *lru.Cache
}

func (l *lruCache) Size() int {
	return l.size
}

func newLRUCache(memory Freeable, r Reporter, size uint) *lruCache {
	lru, _ := lru.New(int(size))
	return &lruCache{memory, r, int(size), lru}
}

func (l *lruCache) Put(k uint64, v interface{}) error {
	if releaseMemoryIfNeeded(l.reporter, l.Free, l.memory.Free) {
		l.cache.Add(k, v)
	}
	return nil
}

func (l *lruCache) Get(k uint64) (interface{}, error) {
	v, ok := l.cache.Get(k)
	if !ok {
		return nil, ErrKeyNotFound
	}

	return v, nil
}

func (l *lruCache) Free() {
	l.cache, _ = lru.New(l.size)
}

func (l *lruCache) Dispose() {
	l.memory = nil
	l.cache = nil
}

type rowsCache struct {
	memory   Freeable
	reporter Reporter
	rows     []Row
	rows2    []Row2
}

func newRowsCache(memory Freeable, r Reporter) *rowsCache {
	return &rowsCache{memory: memory, reporter: r}
}

func (c *rowsCache) Add(row Row) error {
	if !releaseMemoryIfNeeded(c.reporter, c.memory.Free) {
		return ErrNoMemoryAvailable.New()
	}

	c.rows = append(c.rows, row)
	return nil
}

func (c *rowsCache) Get() []Row { return c.rows }

func (c *rowsCache) Add2(row2 Row2) error {
	if !releaseMemoryIfNeeded(c.reporter, c.memory.Free) {
		return ErrNoMemoryAvailable.New()
	}

	c.rows2 = append(c.rows2, row2)
	return nil
}

func (c *rowsCache) Get2() []Row2 {
	return c.rows2
}

func (c *rowsCache) Dispose() {
	c.memory = nil
	c.rows = nil
}

// mapCache is a simple in-memory implementation of a cache
type mapCache struct {
	cache map[uint64]interface{}
}

func (m mapCache) Put(u uint64, i interface{}) error {
	m.cache[u] = i
	return nil
}

func (m mapCache) Get(u uint64) (interface{}, error) {
	v, ok := m.cache[u]
	if !ok {
		return nil, ErrKeyNotFound
	}
	return v, nil
}

func (m mapCache) Size() int {
	return len(m.cache)
}

func NewMapCache() mapCache {
	return mapCache{
		cache: make(map[uint64]interface{}),
	}
}

type historyCache struct {
	memory   Freeable
	reporter Reporter
	cache    map[uint64]interface{}
}

func (h *historyCache) Size() int {
	return len(h.cache)
}

func newHistoryCache(memory Freeable, r Reporter) *historyCache {
	return &historyCache{memory, r, make(map[uint64]interface{})}
}

func (h *historyCache) Put(k uint64, v interface{}) error {
	if !releaseMemoryIfNeeded(h.reporter, h.memory.Free) {
		return ErrNoMemoryAvailable.New()
	}
	h.cache[k] = v
	return nil
}

func (h *historyCache) Get(k uint64) (interface{}, error) {
	v, ok := h.cache[k]
	if !ok {
		return nil, ErrKeyNotFound
	}
	return v, nil
}

func (h *historyCache) Dispose() {
	h.memory = nil
	h.cache = nil
}

// releasesMemoryIfNeeded releases memory if needed using the following steps
// until there is available memory. It returns whether or not there was
// available memory after all the steps.
func releaseMemoryIfNeeded(r Reporter, steps ...func()) bool {
	for _, s := range steps {
		if HasAvailableMemory(r) {
			return true
		}

		s()
		runtime.GC()
	}

	return HasAvailableMemory(r)
}
