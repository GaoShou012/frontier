package frontier

import (
	"container/list"
	"errors"
	"fmt"
	"sync"
)

type ider struct {
	mutex  sync.Mutex
	exists map[int]bool
	li     list.List
}

func (p *ider) init(size int) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	for i := 0; i < size; i++ {
		p.li.PushBack(i)
		p.exists[i] = true
	}
}
func (p *ider) get() (id int, err error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	ele := p.li.Front()
	if ele == nil {
		err = errors.New("No id can be use\n")
		return
	}
	id = ele.Value.(int)
	p.exists[id] = false
	p.li.Remove(ele)
	return
}
func (p *ider) put(id int) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	exists, ok := p.exists[id]
	if !ok {
		panic(fmt.Sprintf("ID(%d)超出预设范围", id))
	}
	if exists {
		panic(fmt.Sprintf("ID(%d)已经存在，不能重复put", id))
	}
	p.exists[id] = true
	p.li.PushFront(id)
}
