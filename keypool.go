package main

import (
	"log"
	"sync/atomic"
)

type KeyPool struct {
	keys  []string
	index uint64
}

func NewKeyPool(key []string) *KeyPool {
	if len(key) == 0 {
		log.Fatal("没有api")
	}
	return &KeyPool{
		keys:  key,
		index: 0,
	}
}

func (p *KeyPool) GetNextKey() string {
	current := atomic.AddUint64(&p.index, 1)
	index := (current - 1) % uint64(len(p.keys))
	return p.keys[index]
}
