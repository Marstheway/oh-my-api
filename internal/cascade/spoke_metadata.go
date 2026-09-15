package cascade

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// errMetadataWriteFailed 表示快照连接写入失败；publisher 立即终止，
// 交由既有断线/重连路径恢复，不做退避重试。
var errMetadataWriteFailed = errors.New("metadata snapshot write failed")

// handleMetadataReject 处理 Hub 回送的同 ID metadata rejection（非致命）。
// 仅匹配最近一次发送的快照 ID；旧 ID 的 rejection 忽略。
func (s *Spoke) handleMetadataReject(id string) {
	s.metadataRejectMu.Lock()
	currentID := s.lastMetadataID
	s.metadataRejectMu.Unlock()
	if id == "" || id != currentID {
		return
	}
	select {
	case s.metadataRejectCh <- struct{}{}:
	default:
	}
}

// runMetadataPublisher 在单个连接上发布 Spoke 元数据快照：
//   - 注册成功后立即发布完整快照
//   - 本地元数据变化（Apply / provider catalog / models.dev 索引替换）
//     经 NotifyMetadataChanged 触发，以规范化后的内容去重
//   - 构造/编码失败或同 ID metadata rejection 保持 dirty，以 1s/2s/4s 最多
//     重试三次；耗尽后等待下一次变化触发或重连重新发布
//   - 任意连接级 WebSocket 写失败不参与同连接重试：幂等关闭当前连接，
//     由 read loop 清理并由 Spoke.Run 按既有机制重连
func (s *Spoke) runMetadataPublisher(sc *spokeConn, disconnected chan struct{}) {
	if s.cfg.MetadataProvider == nil {
		return
	}

	retryDelays := s.metadataRetryDelays()
	var (
		dirty       = true // 注册后立即发布
		publishNow  = true
		forceResend bool
		lastBody    []byte
		retries     int
		timer       *time.Timer
		timerC      <-chan time.Time
	)
	cancelTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		timerC = nil
	}
	scheduleRetry := func() bool {
		cancelTimer()
		if retries >= len(retryDelays) {
			return false
		}
		timer = time.NewTimer(retryDelays[retries])
		timerC = timer.C
		retries++
		return true
	}

	for {
		if publishNow {
			publishNow = false
			published, err := s.publishMetadataStep(sc, &lastBody, &forceResend)
			switch {
			case err == errMetadataWriteFailed:
				// 连接级写失败：不参与同连接重试。幂等关闭当前 WebSocket，
				// 使 read loop 的读立即失败、清理 registered/disconnected 状态，
				// Spoke.Run 按既有机制重连；重连后的首次完整发布重新拥有重试预算。
				sc.closeConn()
				return
			case err != nil:
				// 构造/编码失败：保持 dirty 并退避重试；耗尽后等待下一次触发。
				scheduleRetry()
			case published:
				dirty = false
				forceResend = false
			default:
				// 内容未变化且无待处理拒绝：无需发布。
				dirty = false
				forceResend = false
			}
		}

		select {
		case <-disconnected:
			return
		case <-s.metadataNotify:
			dirty = true
			retries = 0
			cancelTimer()
			publishNow = true
		case <-s.metadataRejectCh:
			// 非致命 rejection：保持连接并进入退避重试，不立即重发。
			dirty = true
			forceResend = true
			scheduleRetry()
		case <-timerC:
			timerC = nil
			if dirty {
				publishNow = true
			}
		}
	}
}

// publishMetadataStep 构造并发送一次元数据快照。
// 返回 (published, err)：
//   - (true, nil)：已发送
//   - (false, nil)：内容未变化且无待处理拒绝，跳过（去重）
//   - (false, errMetadataWriteFailed)：连接级写失败（publisher 幂等关闭连接后终止）
//   - (false, 其它 err)：构造/编码失败（可退避重试）
func (s *Spoke) publishMetadataStep(sc *spokeConn, lastBody *[]byte, forceResend *bool) (bool, error) {
	models := s.cfg.MetadataProvider()
	if models == nil {
		models = []MetadataModel{}
	}
	body, err := json.Marshal(models)
	if err != nil {
		return false, fmt.Errorf("encode metadata snapshot: %w", err)
	}
	if bytes.Equal(body, *lastBody) && !*forceResend {
		return false, nil
	}

	id := uuid.NewString()
	// 先记录 ID，确保对本次快照的 rejection 在写入返回前到达也能被匹配。
	s.metadataRejectMu.Lock()
	s.lastMetadataID = id
	s.metadataRejectMu.Unlock()

	if s.testMetadataWriteHook != nil {
		if err := s.testMetadataWriteHook(); err != nil {
			return false, errMetadataWriteFailed
		}
	}
	if err := sc.writeFrame(Frame{Type: FrameMetadataSnapshot, ID: id, Body: body}); err != nil {
		return false, errMetadataWriteFailed
	}
	*lastBody = body
	*forceResend = false
	return true, nil
}

// metadataRetryDelays 返回发布失败的重试间隔（1s/2s/4s），测试可替换。
func (s *Spoke) metadataRetryDelays() []time.Duration {
	if len(s.testMetadataDelays) > 0 {
		return s.testMetadataDelays
	}
	return []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
}
