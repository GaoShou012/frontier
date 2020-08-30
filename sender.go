package frontier

import (
	"github.com/GaoShou012/tools/logger"
	"sync"
)

type senderJob struct {
	c       *conn
	message []byte
}
type sender struct {
	parallel int
	pool     sync.Pool
	jobs     []chan *senderJob
	params   *DynamicParams
}

func (s *sender) init(parallel int, cacheSize int, params *DynamicParams) {
	s.parallel = parallel
	s.jobs = make([]chan *senderJob, parallel)
	s.pool.New = func() interface{} {
		return new(senderJob)
	}
	s.params = params
	for i := 0; i < parallel; i++ {
		s.jobs[i] = make(chan *senderJob, cacheSize)
		go func(i int) {
			for {
				job := <-s.jobs[i]
				c, message := job.c, job.message
				if c.state == connStateIsWorking {
					err := c.protocol.Writer(c.netConn, message)
					if err != nil {
						if s.params.LogLevel >= logger.LogWarning {
							logger.Println(logger.LogWarning, err)
						}
					}
				} else {
					if s.params.LogLevel >= logger.LogWarning {
						logger.Println(logger.LogWarning, "To send data failed,the conn is not working", c.NetConn().RemoteAddr().String())
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
	index := j.c.id & s.parallel
	s.jobs[index] <- j
}
