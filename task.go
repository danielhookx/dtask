package dtask

import (
	"sync"
	"time"

	log "github.com/golang/glog"
)

const (
	timerFormat      = "2006-01-02 15:04:05"
	infiniteDuration = time.Duration(1<<63 - 1)
)

type Task struct {
	sync.RWMutex
	Key        string
	expire     time.Time
	dur        time.Duration
	fn         func(map[string][]byte)
	protoStore map[string][]byte
	index      int
	next       *Task
}

// Delay delay duration.
func (tk *Task) Delay() time.Duration {
	return time.Until(tk.expire)
}

// ExpireString expire string.
func (tk *Task) ExpireString() string {
	return tk.expire.Format(timerFormat)
}

func (tk *Task) Add(id string, data []byte) {
	tk.Lock()
	tk.protoStore[id] = data
	tk.Unlock()
}

func (tk *Task) Del(id string) {
	tk.Lock()
	delete(tk.protoStore, id)
	tk.Unlock()
}

//---------------------------------------------//
type TaskPool struct {
	lock   sync.Mutex
	free   *Task
	tasks  []*Task
	signal *time.Timer
	num    int
}

func NewTaskPool(num int) (t *TaskPool) {
	t = new(TaskPool)
	t.init(num)
	return t
}

// Init init the task pool.
func (t *TaskPool) Init(num int) {
	t.init(num)
}

func (t *TaskPool) init(num int) {
	t.signal = time.NewTimer(infiniteDuration)
	t.tasks = make([]*Task, 0, num)
	t.num = num
	t.grow()
	go t.start()
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

func (t *TaskPool) get() (tk *Task) {
	if tk = t.free; tk == nil {
		t.grow()
		tk = t.free
	}
	t.free = tk.next
	return
}

func (t *TaskPool) put(tk *Task) {
	tk.fn = nil
	tk.protoStore = nil
	tk.next = t.free
	t.free = tk
}

// Push pushes the element x onto the heap. The complexity is
// O(log(n)) where n = h.Len().
func (t *TaskPool) add(tk *Task) {
	var d time.Duration
	tk.index = len(t.tasks)
	// add to the minheap last node
	t.tasks = append(t.tasks, tk)
	t.up(tk.index)
	if tk.index == 0 {
		// if first node, signal start goroutine
		d = tk.Delay()
		t.signal.Reset(d)
		if Debug {
			log.Infof("timer: add reset delay %d ms", int64(d)/int64(time.Millisecond))
		}
	}
	if Debug {
		log.Infof("timer: push item key: %s, expire: %s, index: %d", tk.Key, tk.ExpireString(), tk.index)
	}
}

func (t *TaskPool) del(tk *Task) {
	var (
		i    = tk.index
		last = len(t.tasks) - 1
	)
	if i < 0 || i > last || t.tasks[i] != tk {
		// already remove, usually by expire
		if Debug {
			log.Infof("timer del i: %d, last: %d, %p", i, last, tk)
		}
		return
	}
	if i != last {
		t.swap(i, last)
		t.down(i, last)
		t.up(i)
	}
	// remove item is the last node
	t.tasks[last].index = -1 // for safety
	t.tasks = t.tasks[:last]
	if Debug {
		log.Infof("timer: remove item key: %s, expire: %s, index: %d", tk.Key, tk.ExpireString(), tk.index)
	}
}

func (t *TaskPool) Add(dur time.Duration, fn func(map[string][]byte)) (tk *Task) {
	t.lock.Lock()
	tk = t.get()
	tk.expire = time.Now().Add(dur)
	tk.fn = fn
	tk.dur = dur
	tk.protoStore = make(map[string][]byte)
	t.add(tk)
	t.lock.Unlock()
	return
}

func (t *TaskPool) Del(tk *Task) {
	t.lock.Lock()
	t.del(tk)
	t.put(tk)
	t.lock.Unlock()
}

// Set update timer data.
func (t *TaskPool) Set(tk *Task, expire time.Duration) {
	t.lock.Lock()
	t.del(tk)
	tk.expire = time.Now().Add(expire)
	t.add(tk)
	t.lock.Unlock()
}

// start start the timer.
func (t *TaskPool) start() {
	for {
		t.expire()
		<-t.signal.C
	}
}

// expire removes the minimum element (according to Less) from the heap.
// The complexity is O(log(n)) where n = max.
// It is equivalent to Del(0).
func (t *TaskPool) expire() {
	var (
		fn func(map[string][]byte)
		tk *Task
		d  time.Duration
	)
	t.lock.Lock()
	for {
		if len(t.tasks) == 0 {
			d = infiniteDuration
			if Debug {
				log.Info("timer: no other instance")
			}
			break
		}
		tk = t.tasks[0]
		if d = tk.Delay(); d > 0 {
			break
		}
		fn = tk.fn
		// let caller put back
		//---
		t.del(tk)
		tk.expire = time.Now().Add(tk.dur)
		t.add(tk)
		//---
		//t.del(tk)
		t.lock.Unlock()
		if fn == nil {
			log.Warning("expire timer no fn")
		} else {
			if Debug {
				log.Infof("timer key: %s, expire: %s, index: %d expired, call fn", tk.Key, tk.ExpireString(), tk.index)
			}
			fn(tk.protoStore)
		}
		t.lock.Lock()
	}
	t.signal.Reset(d)
	if Debug {
		log.Infof("timer: expier reset delay %d ms", int64(d)/int64(time.Millisecond))
	}
	t.lock.Unlock()
}

func (t *TaskPool) up(j int) {
	for {
		i := (j - 1) / 2 // parent
		if i >= j || !t.less(j, i) {
			break
		}
		t.swap(i, j)
		j = i
	}
}

func (t *TaskPool) down(i, n int) {
	for {
		j1 := 2*i + 1
		if j1 >= n || j1 < 0 { // j1 < 0 after int overflow
			break
		}
		j := j1 // left child
		if j2 := j1 + 1; j2 < n && !t.less(j1, j2) {
			j = j2 // = 2*i + 2  // right child
		}
		if !t.less(j, i) {
			break
		}
		t.swap(i, j)
		i = j
	}
}

func (t *TaskPool) less(i, j int) bool {
	return t.tasks[i].expire.Before(t.tasks[j].expire)
}

func (t *TaskPool) swap(i, j int) {
	t.tasks[i], t.tasks[j] = t.tasks[j], t.tasks[i]
	t.tasks[i].index = i
	t.tasks[j].index = j
}
