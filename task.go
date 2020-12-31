package dtask

import (
	"fmt"
	"sync"
	"time"
)

type Task struct {
	sync.RWMutex
	key	string
	ticker *time.Ticker
	done chan bool
	fn	 map[string]func()
	next *Task
}

func (tk *Task) start() {
	defer tk.ticker.Stop()
	for {
		select {
		case <-tk.done:
			fmt.Println("Done!")
			return
		case <-tk.ticker.C:
			tk.RLock()
			for _, fn := range tk.fn {
				select {
				case <-tk.done:
					fmt.Println("Done!")
					return
				default:
					fn()
				}
			}
			tk.RUnlock()
			//fmt.Println("Current time: ", t)
		}
	}
}

func (tk *Task) Add(key string, fn func()) {
	tk.Lock()
	tk.fn[key] = fn
	tk.Unlock()
}

func (tk *Task) Done(id string) {
	tk.Lock()
	delete(tk.fn, id)
	tk.Unlock()
}

//---------------------------------------------//
type TaskPool struct {
	lock sync.Mutex
	free *Task
	num    int
}

func NewTaskPool(num int) (t *TaskPool) {
	t = new(TaskPool)
	t.Init(num)
	return t
}

func (t *TaskPool) Init(num int) {
	t.num = num
	t.grow()
}

func (t *TaskPool) grow() {
	var (
		i   int
		tk  *Task
		tks = make([]Task, t.num)
	)
	t.free = &(tks[0])
	tk = t.free
	for i = 1; i < t.num; i++ {
		tk.next = &(tks[i])
		tk = tk.next
	}
	tk.next = nil
}

func (t *TaskPool) put(tk *Task) {
	tk.fn = nil
	tk.done = nil
	tk.ticker = nil
	tk.next = t.free
	t.free = tk
}

func (t *TaskPool) get() (tk *Task){
	if tk = t.free; tk == nil {
		t.grow()
		tk = t.free
	}
	t.free = tk.next
	return
}

func (t *TaskPool) Add(dur time.Duration) (tk *Task){
	t.lock.Lock()
	tk = t.get()
	tk.ticker = time.NewTicker(dur)
	tk.fn = make(map[string]func())
	tk.done = make(chan bool)

	go tk.start()
	t.lock.Unlock()
	return
}

func (t *TaskPool) Del(tk *Task) {
	t.lock.Lock()
	tk.done<-true
	t.put(tk)
	t.lock.Unlock()
}