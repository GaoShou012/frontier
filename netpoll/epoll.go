// +build linux

package netpoll

import (
	"runtime"
	"sync"
	"wchatv1/src/ider"

	"golang.org/x/sys/unix"
)

// EpollEvent represents epoll events configuration bit mask.
type EpollEvent uint32

// EpollEvents that are mapped to epoll_event.events possible values.
const (
	EPOLLIN      = unix.EPOLLIN
	EPOLLOUT     = unix.EPOLLOUT
	EPOLLRDHUP   = unix.EPOLLRDHUP
	EPOLLPRI     = unix.EPOLLPRI
	EPOLLERR     = unix.EPOLLERR
	EPOLLHUP     = unix.EPOLLHUP
	EPOLLET      = unix.EPOLLET
	EPOLLONESHOT = unix.EPOLLONESHOT

	// _EPOLLCLOSED is a special EpollEvent value the receipt of which means
	// that the epoll instance is closed.
	_EPOLLCLOSED = 0x20
)

// String returns a string representation of EpollEvent.
func (evt EpollEvent) String() (str string) {
	name := func(event EpollEvent, name string) {
		if evt&event == 0 {
			return
		}
		if str != "" {
			str += "|"
		}
		str += name
	}

	name(EPOLLIN, "EPOLLIN")
	name(EPOLLOUT, "EPOLLOUT")
	name(EPOLLRDHUP, "EPOLLRDHUP")
	name(EPOLLPRI, "EPOLLPRI")
	name(EPOLLERR, "EPOLLERR")
	name(EPOLLHUP, "EPOLLHUP")
	name(EPOLLET, "EPOLLET")
	name(EPOLLONESHOT, "EPOLLONESHOT")
	name(_EPOLLCLOSED, "_EPOLLCLOSED")

	return
}

// Epoll represents single epoll instance.
type Epoll struct {
	mu sync.RWMutex

	fd       int
	eventFd  int
	closed   bool
	waitDone chan struct{}

	callbacks map[int]func(EpollEvent)
}

// EpollConfig contains options for Epoll instance configuration.
type EpollConfig struct {
	// OnWaitError will be called from goroutine, waiting for events.
	OnWaitError func(error)
}

func (c *EpollConfig) withDefaults() (config EpollConfig) {
	if c != nil {
		config = *c
	}
	if config.OnWaitError == nil {
		config.OnWaitError = defaultOnWaitError
	}
	return config
}

// EpollCreate creates new epoll instance.
// It starts the wait loop in separate goroutine.
func EpollCreate(c *EpollConfig) (*Epoll, error) {
	config := c.withDefaults()

	fd, err := unix.EpollCreate1(0)
	if err != nil {
		return nil, err
	}

	r0, _, errno := unix.Syscall(unix.SYS_EVENTFD2, 0, 0, 0)
	if errno != 0 {
		return nil, errno
	}
	eventFd := int(r0)

	// Set finalizer for write end of socket pair to avoid data races when
	// closing Epoll instance and EBADF errors on writing ctl bytes from callers.
	err = unix.EpollCtl(fd, unix.EPOLL_CTL_ADD, eventFd, &unix.EpollEvent{
		Events: unix.EPOLLIN,
		Fd:     int32(eventFd),
	})
	if err != nil {
		unix.Close(fd)
		unix.Close(eventFd)
		return nil, err
	}

	ep := &Epoll{
		fd:        fd,
		eventFd:   eventFd,
		callbacks: make(map[int]func(EpollEvent)),
		waitDone:  make(chan struct{}),
	}

	// Run wait loop.
	go ep.wait(config.OnWaitError)

	return ep, nil
}

// closeBytes used for writing to eventfd.
var closeBytes = []byte{1, 0, 0, 0, 0, 0, 0, 0}

// Close stops wait loop and closes all underlying resources.
func (ep *Epoll) Close() (err error) {
	ep.mu.Lock()
	{
		if ep.closed {
			ep.mu.Unlock()
			return ErrClosed
		}
		ep.closed = true

		if _, err = unix.Write(ep.eventFd, closeBytes); err != nil {
			ep.mu.Unlock()
			return
		}
	}
	ep.mu.Unlock()

	<-ep.waitDone

	if err = unix.Close(ep.eventFd); err != nil {
		return
	}

	ep.mu.Lock()
	// Set callbacks to nil preventing long mu.Lock() hold.
	// This could increase the speed of retreiving ErrClosed in other calls to
	// current epoll instance.
	// Setting callbacks to nil is safe here because no one should read after
	// closed flag is true.
	callbacks := ep.callbacks
	ep.callbacks = nil
	ep.mu.Unlock()

	for _, cb := range callbacks {
		if cb != nil {
			cb(_EPOLLCLOSED)
		}
	}

	return
}

// Add adds fd to epoll set with given events.
// Callback will be called on each received event from epoll.
// Note that _EPOLLCLOSED is triggered for every cb when epoll closed.
func (ep *Epoll) Add(fd int, events EpollEvent, cb func(EpollEvent)) (err error) {
	ev := &unix.EpollEvent{
		Events: uint32(events),
		Fd:     int32(fd),
	}

	ep.mu.Lock()
	defer ep.mu.Unlock()

	if ep.closed {
		return ErrClosed
	}
	if _, has := ep.callbacks[fd]; has {
		return ErrRegistered
	}
	ep.callbacks[fd] = cb

	return unix.EpollCtl(ep.fd, unix.EPOLL_CTL_ADD, fd, ev)
}

// Del removes fd from epoll set.
func (ep *Epoll) Del(fd int) (err error) {
	ep.mu.Lock()
	defer ep.mu.Unlock()

	if ep.closed {
		return ErrClosed
	}
	if _, ok := ep.callbacks[fd]; !ok {
		return ErrNotRegistered
	}

	delete(ep.callbacks, fd)

	return unix.EpollCtl(ep.fd, unix.EPOLL_CTL_DEL, fd, nil)
}

// Mod sets to listen events on fd.
func (ep *Epoll) Mod(fd int, events EpollEvent) (err error) {
	ev := &unix.EpollEvent{
		Events: uint32(events),
		Fd:     int32(fd),
	}

	ep.mu.RLock()
	defer ep.mu.RUnlock()

	if ep.closed {
		return ErrClosed
	}
	if _, ok := ep.callbacks[fd]; !ok {
		return ErrNotRegistered
	}

	return unix.EpollCtl(ep.fd, unix.EPOLL_CTL_MOD, fd, ev)
}

const (
	maxWaitEventsBegin = 1024
	maxWaitEventsStop  = 32768
)

type job struct {
	id int
	n  int
}

type block struct {
	events    []unix.EpollEvent
	callbacks []func(event EpollEvent)
}

var blocks []block
var jobs chan *job

func (ep *Epoll) wait(onError func(error)) {
	defer func() {
		if err := unix.Close(ep.fd); err != nil {
			onError(err)
		}
		close(ep.waitDone)
	}()

	// 缓存block
	idPool := ider.IdPool{}
	idPool.Init(1000)
	blocks = make([]block, 1000)
	for i := 0; i < 1000; i++ {
		blocks[i].events = make([]unix.EpollEvent, maxWaitEventsBegin)
		blocks[i].callbacks = make([]func(event EpollEvent), 0, maxWaitEventsBegin)
	}
	jobs = make(chan *job, 10000)

	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			for {
				job := <-jobs
				id, n := job.id, job.n
				blocks[id].callbacks = blocks[id].callbacks[:n]

				ep.mu.RLock()
				for i := 0; i < n; i++ {
					fd := int(blocks[id].events[i].Fd)
					if fd == ep.eventFd { // signal to close
						panic("signal to close")
					}
					blocks[id].callbacks[i] = ep.callbacks[fd]
				}
				ep.mu.RUnlock()

				for i := 0; i < n; i++ {
					if cb := blocks[id].callbacks[i]; cb != nil {
						cb(EpollEvent(blocks[id].events[i].Events))
						blocks[id].callbacks[i] = nil
					}
				}

				if err := idPool.Put(id); err != nil {
					panic(err)
				}
			}
		}()
	}

	for {
		id, err := idPool.Get()
		if err != nil {
			panic(err)
		}

		n, err := unix.EpollWait(ep.fd, blocks[id].events, -1)
		if err != nil {
			if temporaryErr(err) {
				idPool.Put(id)
				continue
			}
			onError(err)
			return
		}

		jobs <- &job{
			id: id,
			n:  n,
		}
	}

	events := make([]unix.EpollEvent, maxWaitEventsBegin)
	callbacks := make([]func(EpollEvent), 0, maxWaitEventsBegin)
	for {
		n, err := unix.EpollWait(ep.fd, events, -1)
		if err != nil {
			if temporaryErr(err) {
				continue
			}
			onError(err)
			return
		}

		callbacks = callbacks[:n]

		ep.mu.RLock()
		for i := 0; i < n; i++ {
			fd := int(events[i].Fd)
			if fd == ep.eventFd { // signal to close
				panic("signal to close")
				ep.mu.RUnlock()
				return
			}
			callbacks[i] = ep.callbacks[fd]
		}
		ep.mu.RUnlock()

		for i := 0; i < n; i++ {
			if cb := callbacks[i]; cb != nil {
				cb(EpollEvent(events[i].Events))
				callbacks[i] = nil
			}
		}

		if n == len(events) && n*2 <= maxWaitEventsStop {
			events = make([]unix.EpollEvent, n*2)
			callbacks = make([]func(EpollEvent), 0, n*2)
		}
	}
}
