package frontier

const (
	ConnEventTypeInsert = iota
	ConnEventTypeDelete
	ConnEventTypeUpdate
	ConnEventTypeClose
)

type connEvent struct {
	Type int
	Conn *conn
}
