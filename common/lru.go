package common

import (
	"errors"
	"sync"
)

type LRU[T string] struct {
	data    map[T]bool
	queue   *Queue[T]
	lock    *sync.Mutex
	maxSize int
}

func (lru *LRU[T]) Init(maxSize int) {
	lru.maxSize = maxSize
	lru.data = make(map[T]bool, lru.maxSize)
	lru.queue = NewQueue[T](lru.maxSize, maxSize)
	lru.lock = new(sync.Mutex)
}

func (lru *LRU[T]) Add(key T) (err error) {
	lru.lock.Lock()
	defer lru.lock.Unlock()
	_, exist := lru.data[key]
	if exist == true {
		return errors.New("exising digest, possible replay attack")
	}

	if lru.queue.Size() == lru.maxSize {
		delete(lru.data, lru.queue.Pop())
	}

	lru.data[key] = true
	return lru.queue.Push(key)
}
