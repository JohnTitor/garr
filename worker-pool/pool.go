// Copyright 2022 LINE Corporation
//
// LINE Corporation licenses this file to you under the Apache License,
// version 2.0 (the "License"); you may not use this file except in compliance
// with the License. You may obtain a copy of the License at:
//
//   https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

package workerpool

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

var numCPU int

func init() {
	numCPU = runtime.NumCPU()
}

// TaskResult represents result of task.
type TaskResult struct {
	Result interface{}
	Err    error
}

// Task represents a task.
type Task struct {
	ctx      context.Context
	executor func(context.Context) (interface{}, error)
	future   chan *TaskResult
}

// NewTask creates new task.
func NewTask(ctx context.Context, executor func(context.Context) (interface{}, error)) *Task {
	return &Task{
		ctx:      ctx,
		executor: executor,
		future:   make(chan *TaskResult, 1),
	}
}

// Execute task.
func (t *Task) Execute() {
	var result interface{}
	var err error

	if t.executor != nil {
		result, err = t.executor(t.ctx)
	}

	t.future <- &TaskResult{Result: result, Err: err}
}

// Result pushed via channel
func (t *Task) Result() <-chan *TaskResult {
	return t.future
}

// Option represents worker pool's option.
type Option struct {
	// DisableAutoStart disables underlying workers from starting/spawning automatically.
	DisableAutoStart bool `yaml:"disable_auto_start" json:"disable_auto_start"`
	// NumberWorker represents number of workers.
	// Default: runtime.NumCPU()
	NumberWorker int `yaml:"number_worker" json:"number_worker"`
	// ExpandableLimit limits number of workers to be expanded on demand.
	// Default: 0 (no expandable)
	ExpandableLimit int32 `yaml:"expandable_limit" json:"expandable_limit"`
	// ExpandedLifetime represents lifetime of expanded worker (in nanoseconds).
	// Default: 1 minute
	ExpandedLifetime time.Duration `yaml:"expanded_lifetime" json:"expanded_lifetime"`
}

func (o *Option) normalize() {
	if o.NumberWorker <= 0 {
		o.NumberWorker = numCPU
	}

	if o.ExpandableLimit < 0 {
		o.ExpandableLimit = 0
	}

	if o.ExpandedLifetime <= 0 {
		o.ExpandedLifetime = time.Minute
	}
}

// Pool is a lightweight worker pool with capable of auto-expand on demand.
type Pool struct {
	ctx    context.Context
	cancel context.CancelFunc

	opt Option

	wg        sync.WaitGroup
	taskQueue chan *Task
	expanded  int32

	state uint32 // 0: not start, 1: started, 2: stopped
}

// NewPool creates new worker pool.
func NewPool(ctx context.Context, opt Option) (p *Pool) {
	if ctx == nil {
		ctx = context.Background()
	}

	// normalize option
	opt.normalize()

	// set up pool
	p = &Pool{
		opt:       opt,
		taskQueue: make(chan *Task, 1),
	}
	p.ctx, p.cancel = context.WithCancel(ctx)

	// start underlying workers?
	if !opt.DisableAutoStart {
		p.Start()
	}

	return
}

// Start underlying workers.
func (p *Pool) Start() {
	if atomic.CompareAndSwapUint32(&p.state, 0, 1) {
		numWorker := p.opt.NumberWorker

		p.wg.Add(numWorker)
		for i := 0; i < numWorker; i++ {
			go p.worker()
		}
	}
}

// Stop worker. Wait all task done.
func (p *Pool) Stop() {
	if atomic.CompareAndSwapUint32(&p.state, 1, 2) || atomic.CompareAndSwapUint32(&p.state, 0, 2) {
		// cancel context
		p.cancel()

		// wait child workers
		close(p.taskQueue)
		p.wg.Wait()
	}
}

// Execute a task.
func (p *Pool) Execute(exec func(context.Context) (interface{}, error)) (t *Task) {
	return p.ExecuteWithCtx(p.ctx, exec)
}

// ExecuteWithCtx a task with custom context.
func (p *Pool) ExecuteWithCtx(ctx context.Context, exec func(context.Context) (interface{}, error)) (t *Task) {
	if ctx == nil {
		ctx = p.ctx
	}
	t = NewTask(ctx, exec)
	p.Do(t)
	return
}

// TryExecute tries to execute a task. If task queue is full, returns immediately and
// addedToQueue is false.
func (p *Pool) TryExecute(exec func(context.Context) (interface{}, error)) (t *Task, addedToQueue bool) {
	return p.TryExecuteWithCtx(p.ctx, exec)
}

// TryExecuteWithCtx tries to execute a task with custom context. If task queue is full, returns immediately and
// addedToQueue is false.
func (p *Pool) TryExecuteWithCtx(ctx context.Context, exec func(context.Context) (interface{}, error)) (t *Task, addedToQueue bool) {
	if ctx == nil {
		ctx = p.ctx
	}
	t = NewTask(ctx, exec)
	addedToQueue = p.TryDo(t)
	return
}

// Do a task.
func (p *Pool) Do(t *Task) {
	if t != nil {
		if t.ctx == nil {
			t.ctx = p.ctx
		}

		if p.opt.ExpandableLimit == 0 {
			p.push(t)
		} else {
			select {
			case p.taskQueue <- t:
			default:
				if atomic.AddInt32(&p.expanded, 1) <= p.opt.ExpandableLimit {
					p.wg.Add(1)
					go p.expandedWorker()
				} else {
					atomic.AddInt32(&p.expanded, -1)
				}

				// push again
				p.push(t)
			}
		}
	}
}

func (p *Pool) push(t *Task) {
	select {
	case <-p.ctx.Done():
		t.future <- &TaskResult{Err: p.ctx.Err()}

	case <-t.ctx.Done():
		t.future <- &TaskResult{Err: t.ctx.Err()}

	case p.taskQueue <- t:
	}
}

// TryDo tries to execute a task. If task queue is full, returns immediately and
// addedToQueue is false.
func (p *Pool) TryDo(t *Task) (addedToQueue bool) {
	if t != nil {
		if t.ctx == nil {
			t.ctx = p.ctx
		}

		select {
		case <-p.ctx.Done():
			t.future <- &TaskResult{Err: p.ctx.Err()}

		case <-t.ctx.Done():
			t.future <- &TaskResult{Err: t.ctx.Err()}

		case p.taskQueue <- t:
			addedToQueue = true

		default:
		}
	}
	return
}

func (p *Pool) worker() {
	for task := range p.taskQueue {
		task.Execute()
	}
	p.wg.Done()
}

func (p *Pool) expandedWorker() {
	lifetime := p.opt.ExpandedLifetime
	timer := time.NewTimer(lifetime)
	defer func() {
		p.wg.Done()
		atomic.AddInt32(&p.expanded, -1)
	}()

	for {
		select {
		case task, ok := <-p.taskQueue:
			stopTimer(timer)

			if !ok {
				return
			}

			// execute task and expand the lifetime
			task.Execute()
			timer.Reset(lifetime)

		case <-timer.C:
			return
		}
	}
}

func stopTimer(t *time.Timer) {
	if !t.Stop() {
		<-t.C
	}
}
