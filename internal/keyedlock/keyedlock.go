// Package keyedlock 提供按 key 串行的互斥锁。
//
// 用途：工单流程里「同一个用户同时只能开一单」「同一张工单的关闭/锁定/重开
// 必须串行」这类约束只需要按 key 互斥，而不应该让不同 key 互相排队——
// 全局锁会让两名用户开单、两位管理员关单彼此等待，表现为「点一下要等很久」。
package keyedlock

import "sync"

// Locks 是按 key 分组互斥锁的集合，零值即可使用。
type Locks struct {
	mu    sync.Mutex
	locks map[string]*entry
}

type entry struct {
	mu sync.Mutex
	// refs 统计正在持有/等待该 key 的调用数，归零后回收，避免 map 无限增长。
	refs int
}

// Lock 获取 key 对应的锁，返回释放函数（必须调用）。
func (l *Locks) Lock(key string) func() {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*entry)
	}
	item, ok := l.locks[key]
	if !ok {
		item = &entry{}
		l.locks[key] = item
	}
	item.refs++
	l.mu.Unlock()

	item.mu.Lock()

	return func() {
		item.mu.Unlock()
		l.mu.Lock()
		item.refs--
		if item.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}
