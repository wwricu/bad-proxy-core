package common

import (
	"errors"
	"sync"
)

type Queue[T comparable] struct {
	data    []T
	head    int
	tail    int
	size    int
	maxSize int
	lock    sync.Mutex
}

func NewQueue[T comparable](maxSize int, initSize int) *Queue[T] {
	if initSize <= 0 {
		panic("init size must be positive")
	}
	return &Queue[T]{
		data:    make([]T, initSize),
		maxSize: maxSize,
	}
}

// Push appends at the back. A full ring expands before it writes, so a failed
// push leaves the queue untouched; with an explicit size, full is
// distinguishable from empty even though both keep head == tail on the wire.
func (queue *Queue[T]) Push(value T) (err error) {
	queue.lock.Lock()
	defer queue.lock.Unlock()
	if queue.size == cap(queue.data) {
		if err = queue.expand(); err != nil {
			return err
		}
	}
	queue.data[queue.tail] = value
	queue.tail = (queue.tail + 1) % cap(queue.data)
	queue.size++
	return nil
}

func (queue *Queue[T]) Pop() (element T) { // from front
	queue.lock.Lock()
	defer queue.lock.Unlock()

	if queue.size == 0 {
		panic("no element in the list, should never happen")
	}
	element = queue.data[queue.head]
	queue.head = (queue.head + 1) % cap(queue.data)
	queue.size--
	return
}

func (queue *Queue[T]) Size() int {
	queue.lock.Lock()
	defer queue.lock.Unlock()
	return queue.size
}

func (queue *Queue[T]) expand() (err error) {
	capacity := cap(queue.data)
	if queue.maxSize > 0 && capacity == queue.maxSize {
		return errors.New("reach max size")
	}
	newCapacity := capacity * 2
	if queue.maxSize > 0 && newCapacity > queue.maxSize {
		newCapacity = queue.maxSize
	}
	data := make([]T, newCapacity)
	if queue.head < queue.tail {
		copy(data, queue.data[queue.head:queue.tail])
	} else {
		copy(data, queue.data[queue.head:])
		copy(data[capacity-queue.head:], queue.data[:queue.tail])
	}
	queue.head = 0
	queue.tail = capacity // expand runs only when size == capacity
	queue.data = data
	return
}
