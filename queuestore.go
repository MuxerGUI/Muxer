package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const queueStoreName = "queue.json"

var queueStoreDir = func() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

func queueStorePath() string { return filepath.Join(queueStoreDir(), queueStoreName) }

var queueStoreWarnOnce sync.Once

func (a *App) warnQueueStore(err error) {
	queueStoreWarnOnce.Do(func() {
		a.log("warn", fmt.Sprintf("队列落盘失败 %s: %v", queueStorePath(), err))
	})
}

func (a *App) saveQueueSnapshot(tasks []*QueueTask) {
	path := queueStorePath()

	if len(tasks) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			a.warnQueueStore(err)
		}
		return
	}

	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		a.warnQueueStore(err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		a.warnQueueStore(err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		a.warnQueueStore(err)
	}
}

func (a *App) loadQueueSnapshot() string {
	data, err := os.ReadFile(queueStorePath())
	if err != nil {
		return ""
	}
	var tasks []*QueueTask
	if err := json.Unmarshal(data, &tasks); err != nil {
		return fmt.Sprintf("队列文件解析失败，已按空队列启动（文件保留在 %s 供排查）: %v",
			queueStorePath(), err)
	}
	if len(tasks) == 0 {
		return ""
	}

	var pending, interrupted, other int
	for _, t := range tasks {
		switch t.Status {
		case "running":
			t.Status = "failed"
			t.Err = "上次运行时程序异常退出，成品可能不完整；确认无误后可点「重置失败」重新执行"
			interrupted++
		case "pending":
			pending++
		default:
			other++
		}
	}

	a.queueMu.Lock()
	a.queue = tasks
	a.queueMu.Unlock()

	msg := fmt.Sprintf("已从上次会话恢复队列：%d 个任务（待执行 %d", len(tasks), pending)
	if interrupted > 0 {
		msg += fmt.Sprintf("，异常中断 %d", interrupted)
	}
	if other > 0 {
		msg += fmt.Sprintf("，其余 %d", other)
	}
	return msg + "）"
}
