package frontier

import "github.com/prometheus/client_golang/prometheus"

var (
	// 连接数量
	Connections prometheus.Gauge

	// 写超时的连接
	ConnectionsOfWritingTimeout prometheus.Gauge
)

func init() {
	Connections = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "frontier",
		Name:      "connections",
		Help:      "",
	})
	prometheus.MustRegister(Connections)
	
	ConnectionsOfWritingTimeout = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "frontier",
		Name:      "connections_of_writing_timeout",
		Help:      "",
	})
	prometheus.MustRegister(ConnectionsOfWritingTimeout)
}
