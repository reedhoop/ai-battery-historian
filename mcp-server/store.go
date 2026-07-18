package main

import (
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// Store is a thread-safe, capacity-bounded LRU cache of analysis results.
// A single result can be several MB, so we cap the number of entries and
// evict the least-recently-used on overflow (no persistence; clears on restart).
type Store struct {
	mu  sync.Mutex
	m   map[string]*list.Element
	ll  *list.List // front = most recently used; back = least recently used
	cap int
}

type entry struct {
	id  string
	val *UploadResponseCompare
}

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
func (s *Store) Put(v *UploadResponseCompare) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := genID()
	el := s.ll.PushFront(&entry{id: id, val: v})
	s.m[id] = el
	if s.ll.Len() > s.cap {
		oldest := s.ll.Back()
		if oldest != nil {
			s.ll.Remove(oldest)
			delete(s.m, oldest.Value.(*entry).id)
		}
	}
	return id
}

// Get returns the value for id and marks it most-recently-used.
func (s *Store) Get(id string) (*UploadResponseCompare, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.m[id]
	if !ok {
		return nil, false
	}
	s.ll.MoveToFront(el)
	return el.Value.(*entry).val, true
}

// Len returns the current number of cached entries.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ll.Len()
}
