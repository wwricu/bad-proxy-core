package common

import (
	"errors"
	"sync"
)

type LRU struct {
	data    map[string]bool
	queue   *Queue[string]
	lock    sync.Mutex
	maxSize int
}

func NewLRU(maxSize int) *LRU {
	return &LRU{
		data:    make(map[string]bool, maxSize),
		queue:   NewQueue[string](maxSize, maxSize),
		maxSize: maxSize,
	}
}

func (lru *LRU) Add(key string) error {
	lru.lock.Lock()
	defer lru.lock.Unlock()
	if _, exist := lru.data[key]; exist {
		return errors.New("existing digest, possible replay attack")
	}

	if lru.queue.Size() == lru.maxSize {
		delete(lru.data, lru.queue.Pop())
	}
	if err := lru.queue.Push(key); err != nil {
		return err
	}
	lru.data[key] = true
	return nil
}
