package cascade

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type spokeConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func newSpokeConn(conn *websocket.Conn) *spokeConn {
	return &spokeConn{conn: conn}
}

func (c *spokeConn) writeFrame(frame Frame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := EncodeFrame(frame)
	if err != nil {
		return err
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(defaultWriteWait))
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// closeConn 幂等关闭底层连接。publisher 在检测到连接级写失败时调用，
// 使 read loop 的 ReadMessage 立即失败并触发既有断线/重连清理；
// 重复关闭（例如 read loop 已退出）安全，gorilla 对重复 Close 返回错误但不 panic。
func (c *spokeConn) closeConn() {
	if c == nil || c.conn == nil {
		return
	}
	_ = c.conn.Close()
}
