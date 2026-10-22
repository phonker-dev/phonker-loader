package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
)

// operation — одна загрузка или задача бота: свои процессы, своя временная папка
// и своя отмена. В оконном режиме одновременно может идти несколько загрузок и бот,
// поэтому отмена одной не должна задевать остальные.
type operation struct {
	job       jobObject
	cancelled atomic.Bool
	work      string // промежуточные файлы этой операции
}

var opSeq atomic.Int64

func newOperation() *operation {
	op := &operation{work: filepath.Join(tools.work, fmt.Sprintf("op%d", opSeq.Add(1)))}
	op.job.create(false)
	_ = os.MkdirAll(op.work, 0o755)
	return op
}

// cancel останавливает все процессы операции (yt-dlp, ffmpeg и их дочерние).
func (o *operation) cancel() {
	if o == nil {
		return
	}
	o.cancelled.Store(true)
	o.job.terminate()
}

// aborted — операция отменена (или нажат Ctrl+C в консольном режиме).
func (o *operation) aborted() bool {
	return cancelled.Load() || (o != nil && o.cancelled.Load())
}

// finish освобождает job и удаляет промежуточные файлы.
func (o *operation) finish() {
	if o == nil {
		return
	}
	o.job.close()
	_ = os.RemoveAll(o.work)
}
