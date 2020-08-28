package frontier

import (
	"github.com/golang/glog"
	"sync"
)

type senderJob struct {
	c       *conn
	message []byte
}
type sender struct {
	parallel int
	pool     sync.Pool

	// mod算法区分conn的数据流，保持conn的数据顺序
	jobs []chan *senderJob
}

func (s *sender) init(parallel int, cacheSize int, logLevel *int) {
	s.parallel = parallel
	s.jobs = make([]chan *senderJob, parallel)
	s.pool.New = func() interface{} {
		return new(senderJob)
	}
	for i := 0; i < parallel; i++ {
		s.jobs[i] = make(chan *senderJob, cacheSize)
		go func(i int) {
			for {
				job := <-s.jobs[i]
				c, message := job.c, job.message
				if c.state == connStateIsWorking {
					if err := c.protocol.Writer(c.netConn, message); err != nil && *logLevel > LogLevelWarning {
						glog.Warningln(err)
					}
				} else {
					if *logLevel > LogLevelWarning {
						glog.Warningln("连接不是working状态，发送数据失败", c.NetConn().RemoteAddr().String())
					}
				}
				s.pool.Put(job)
			}
		}(i)
	}
}

func (s *sender) push(c *conn, message []byte) {
	j := s.pool.Get().(*senderJob)
	j.c, j.message = c, message
	s.jobs[j.c.id%s.parallel] <- j
}
