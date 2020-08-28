package frontier

const (
	ConnEventTypeInsert = iota
	ConnEventTypeDelete
	ConnEventTypeUpdate
)

type connEvent struct {
	Type int
	Conn *conn
}