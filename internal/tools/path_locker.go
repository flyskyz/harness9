// 路径级读写锁工具（Path-Level Read-Write Lock）。
//
// 本文件提供基于文件路径粒度的 RWMutex + 引用计数锁，用于保护并发文件操作。
// 与全局互斥锁相比，路径级锁允许不同路径的操作完全并发执行，仅在操作同一路径
// 时才发生锁竞争，大幅提升了多工具并发执行时的吞吐量。
//
// # 锁策略
//
//   - 读锁（RLockPath）：多个 goroutine 可同时读取同一文件，共享访问。
//   - 写锁（LockPath）：写操作排他，阻塞所有其他读写操作。
//
// # 引用计数与内存安全
//
// RLockPath / LockPath 每次被调用时，会递增目标路径的引用计数（ref）。
// 对应的解锁函数执行后，引用计数递减。当 ref 归零时，该路径的锁条目
// 从全局 map 中自动删除，防止内存泄漏（Memory Leak）和 map 无限膨胀。
package tools

import (
	"path/filepath"
	"sync"
)

// pathLock 封装每个文件路径的 RWMutex + 引用计数。
// ref 追踪当前有多少 goroutine 正在使用该锁（无论是读还是写）。
// 当 ref 降为 0 时，该条目从全局 map 中删除，避免内存泄漏。
type pathLock struct {
	rw  *sync.RWMutex
	ref int
}

var (
	pathLocksMu sync.Mutex
	pathLocks   = make(map[string]*pathLock) // key: filepath.Clean(path)
)

// getOrCreatePathLock 获取或创建指定路径的 pathLock，并将引用计数 +1。
// 调用方必须在操作完成后调用 releasePathLock。
func getOrCreatePathLock(path string) *pathLock {
	normalized := filepath.Clean(path)

	pathLocksMu.Lock()
	l, ok := pathLocks[normalized]
	if !ok {
		l = &pathLock{rw: &sync.RWMutex{}}
		pathLocks[normalized] = l
	}
	l.ref++
	pathLocksMu.Unlock()

	return l
}

// releasePathLock 将指定路径的引用计数 -1，减到 0 时从全局 map 中删除。
func releasePathLock(path string, l *pathLock) {
	normalized := filepath.Clean(path)

	pathLocksMu.Lock()
	l.ref--
	if l.ref == 0 {
		delete(pathLocks, normalized)
	}
	pathLocksMu.Unlock()
}

// RLockPath 对指定路径获取读锁（共享锁）。
// 多个读操作可以同时持有该锁，写操作会等待所有读锁释放。
// 返回一个解锁函数，典型用法：
//
//	unlock := RLockPath(path)
//	defer unlock()
func RLockPath(path string) func() {
	l := getOrCreatePathLock(path)
	l.rw.RLock()
	return func() {
		l.rw.RUnlock()
		releasePathLock(path, l)
	}
}

// LockPath 对指定路径获取写锁（排他锁）。
// 写锁会阻塞所有其他读锁和写锁，确保原子性。
// 返回一个解锁函数，典型用法：
//
//	unlock := LockPath(path)
//	defer unlock()
func LockPath(path string) func() {
	l := getOrCreatePathLock(path)
	l.rw.Lock()
	return func() {
		l.rw.Unlock()
		releasePathLock(path, l)
	}
}
