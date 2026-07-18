// Copyright 2016 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/reedhoop/ai-battery-historian/analyzer"
)

// storedItem is one cached analysis (a single report, or a comparison).
type storedItem struct {
	Results analyzer.AnalysisResults
	Compare *analyzer.CompareResult // non-nil only for compare_bugreports
}

// mcpStore is the package-level cache, wired in startMCPServer.
var mcpStore *Store

// Store is a thread-safe, capacity-bounded LRU cache of analysis results.
// A single result can be several MB, so we cap the number of entries and
// evict the least-recently-used on overflow (no persistence; clears on restart).
type Store struct {
	mu  sync.Mutex
	m   map[string]*list.Element
	ll  *list.List // front = most recently used; back = least recently used
	cap int
}

type storeEntry struct {
	id  string
	val *storedItem
}

// NewStore creates an LRU store with the given capacity (default 20).
func NewStore(capacity int) *Store {
	if capacity <= 0 {
		capacity = 20
	}
	return &Store{
		m:   make(map[string]*list.Element),
		ll:  list.New(),
		cap: capacity,
	}
}

func genID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback is not cryptographically strong but sufficient for a cache key.
		return "00000000"
	}
	return hex.EncodeToString(b)
}

// Put stores v and returns its generated id.
func (s *Store) Put(v *storedItem) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := genID()
	el := s.ll.PushFront(&storeEntry{id: id, val: v})
	s.m[id] = el
	if s.ll.Len() > s.cap {
		if oldest := s.ll.Back(); oldest != nil {
			s.ll.Remove(oldest)
			delete(s.m, oldest.Value.(*storeEntry).id)
		}
	}
	return id
}

// Get returns the value for id and marks it most-recently-used.
func (s *Store) Get(id string) (*storedItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.m[id]
	if !ok {
		return nil, false
	}
	s.ll.MoveToFront(el)
	return el.Value.(*storeEntry).val, true
}

// Len returns the current number of cached entries.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ll.Len()
}
