package frontier

import "github.com/prometheus/client_golang/prometheus"

var (
	// 连接数量
	Connections prometheus.Gauge

	// 发送器协程数量
	numberOfSendersOfConnections prometheus.Gauge

	// 写超时的连接
	ConnectionsOfWritingTimeout prometheus.Gauge

	// 弃掉消息数量
	ThrowAwayMessage prometheus.Counter
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

	ThrowAwayMessage = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "frontier",
		Name:      "throw_away_message",
		Help:      "",
	})
	prometheus.MustRegister(ThrowAwayMessage)

	numberOfSendersOfConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "frontier",
		Name:      "number_of_senders_of_connections",
		Help:      "",
	})
}
