// 路径级读写锁工具单元测试（Path-Level Read-Write Lock Tests）。
//
// 测试覆盖范围：
//   - 基本读锁共享行为：多个 goroutine 可同时持有同一路径的读锁
//   - 写锁排他行为：写锁阻塞所有其他读写操作
//   - 读-写互斥：读锁与写锁互斥，写锁与读锁互斥
//   - 引用计数正确归零：解锁后 ref 归零，不泄漏 map 条目
//   - 不同路径隔离性：不同路径的锁互不影响
//   - 并发读写正确性：多种并发模式下数据一致
package tools

import (
	"sync"
	"testing"
	"time"
)

// TestRLockPath_MultipleReaders 验证多个 goroutine 可同时持有同一路径的读锁。
// 如果读锁不是共享的，测试将因无法同时获取两把读锁而超时。
func TestRLockPath_MultipleReaders(t *testing.T) {
	const path = "/tmp/test_read_share"

	unlock1 := RLockPath(path)
	unlock2 := RLockPath(path)

	// 若能同时持有两把读锁，说明读锁是共享的（Shared Lock）。
	// 这里不需要额外断言，能执行到此行即表示成功。
	unlock1()
	unlock2()
}

// TestLockPath_ExclusiveWriter 验证写锁的排他性：写锁持有期间，其他写锁无法获取。
// 通过两个 goroutine 协同操作，验证锁的正确排他行为。
func TestLockPath_ExclusiveWriter(t *testing.T) {
	const path = "/tmp/test_write_exclusive"

	var mu sync.Mutex
	writerDone := false

	// 获取写锁（write lock acquired）
	unlock1 := LockPath(path)

	go func() {
		// 尝试获取同一路径的写锁，由于 unlock1 尚未释放，此操作应阻塞。
		// 为确保测试确定性，用 50ms 超时来检测。
		unlock2 := LockPath(path)

		mu.Lock()
		writerDone = true
		mu.Unlock()

		unlock2()
	}()

	// 给 goroutine 50ms 时间尝试获取写锁
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if writerDone {
		mu.Unlock()
		unlock1()
		t.Fatal("第二个写锁应在第一个写锁释放前被阻塞")
	}
	mu.Unlock()

	// 释放第一个写锁，让 goroutine 继续
	unlock1()

	// 等待 goroutine 获取写锁并完成
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if !writerDone {
		mu.Unlock()
		t.Fatal("第二个写锁应在第一个写锁释放后成功获取")
	}
	mu.Unlock()
}

// TestReadWriteLock_MutualExclusion 验证读锁与写锁互斥。
// 读锁持有期间，写锁应被阻塞。
func TestReadWriteLock_MutualExclusion(t *testing.T) {
	const path = "/tmp/test_read_write_mutex"

	var mu sync.Mutex
	writeAcquired := false

	// 先获取读锁
	unlockRead := RLockPath(path)

	go func() {
		// 尝试获取写锁，应被读锁阻塞
		unlockWrite := LockPath(path)

		mu.Lock()
		writeAcquired = true
		mu.Unlock()

		unlockWrite()
	}()

	// 给 goroutine 50ms 尝试获取写锁
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if writeAcquired {
		mu.Unlock()
		unlockRead()
		t.Fatal("写锁应在读锁释放前被阻塞")
	}
	mu.Unlock()

	// 释放读锁
	unlockRead()

	// 等待写锁被获取
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if !writeAcquired {
		mu.Unlock()
		t.Fatal("写锁应在读锁释放后成功获取")
	}
	mu.Unlock()
}

// TestPathLocker_RefCounting 验证引用计数正确归零。
// 多次加锁解锁后，确认 map 中的条目被正确清理。
func TestPathLocker_RefCounting(t *testing.T) {
	const path = "/tmp/test_refcount"

	// 获取 3 个读锁，引用计数预期为 3
	unlock1 := RLockPath(path)
	unlock2 := RLockPath(path)
	unlock3 := RLockPath(path)

	// 释放 2 个，引用计数降为 1
	unlock1()
	unlock2()

	// 此时不应删除 map 条目（尚有 1 个引用）
	pathLocksMu.Lock()
	l, exists := pathLocks[path]
	refCount := 0
	if exists {
		refCount = l.ref
	}
	pathLocksMu.Unlock()

	if !exists {
		t.Fatal("引用计数应在尚有活跃读者时保留 map 条目")
	}
	if refCount != 1 {
		t.Fatalf("期望引用计数为 1，实际为 %d", refCount)
	}

	// 释放最后一个锁，引用计数归零，条目应被删除
	unlock3()

	pathLocksMu.Lock()
	_, exists = pathLocks[path]
	pathLocksMu.Unlock()

	if exists {
		t.Fatal("引用计数归零后，map 条目应被删除")
	}
}

// TestPathLocker_IsolatedPaths 验证不同路径的锁互不影响。
// 对一个路径加锁不应阻塞对另一路径的加锁操作。
func TestPathLocker_IsolatedPaths(t *testing.T) {
	const pathA = "/tmp/test_path_a"
	const pathB = "/tmp/test_path_b"

	// 对 pathA 加写锁
	unlockA := LockPath(pathA)

	// pathB 应能正常获取读锁（互不影响）
	unlockB := RLockPath(pathB)
	unlockB()

	// pathB 应能正常获取写锁（互不影响）
	unlockB = LockPath(pathB)
	unlockB()

	unlockA()
}

// TestPathLocker_ConcurrentReadWrite 验证并发场景下读写锁的正确性。
// 模拟多个读操作和写操作在同一个路径上并发执行，
// 写锁排他、读锁共享的行为应始终保持一致。
func TestPathLocker_ConcurrentReadWrite(t *testing.T) {
	const path = "/tmp/test_concurrent"

	var (
		mu      sync.Mutex
		readers int
		writes  int
		overlap bool
	)

	var wg sync.WaitGroup

	// 启动 5 个并发的读-写对
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// 读操作
			unlockR := RLockPath(path)

			mu.Lock()
			readers++
			if writes > 0 {
				overlap = true
			}
			mu.Unlock()

			// 模拟读操作耗时
			time.Sleep(10 * time.Millisecond)

			mu.Lock()
			readers--
			mu.Unlock()

			unlockR()
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()

			// 写操作
			unlockW := LockPath(path)

			mu.Lock()
			writes++
			if readers > 0 {
				overlap = true
			}
			mu.Unlock()

			// 模拟写操作耗时
			time.Sleep(10 * time.Millisecond)

			mu.Lock()
			writes--
			mu.Unlock()

			unlockW()
		}()
	}

	wg.Wait()

	if overlap {
		t.Fatal("读写操作不应同时发生：写锁持有期间不应有读锁，反之亦然")
	}
}

// TestPathLocker_ConcurrentReaders 验证大量并发读者能共享同一路径的读锁。
func TestPathLocker_ConcurrentReaders(t *testing.T) {
	const path = "/tmp/test_concurrent_readers"

	var wg sync.WaitGroup
	readerCount := 10

	// 使用 channel 统计同时活跃的读者数
	activeReaders := make(chan int, readerCount)

	for i := 0; i < readerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			unlock := RLockPath(path)

			// 报告当前活跃读者数
			pathLocksMu.Lock()
			l, _ := pathLocks[path]
			c := 0
			if l != nil {
				c = l.ref
			}
			pathLocksMu.Unlock()
			activeReaders <- c

			time.Sleep(10 * time.Millisecond)
			unlock()
		}()
	}

	wg.Wait()
	close(activeReaders)

	// 验证至少有一次有多个读者同时活跃
	maxReaders := 0
	for c := range activeReaders {
		if c > maxReaders {
			maxReaders = c
		}
	}
	if maxReaders < 2 {
		t.Fatalf("期望至少 2 个读者同时活跃，实际最大值为 %d", maxReaders)
	}
}
