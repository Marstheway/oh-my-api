package cascade

import (
	"encoding/json"
	"fmt"
)

// parseMetadataSnapshot 校验并解析 metadata snapshot frame，返回模型名 → context_length 映射。
// 全有或全无：任一条目不合法（空/重复/超长模型名、非正 context_length）或超过
// 条目/名称上限时返回错误，调用方必须保留上一份有效快照并回送同 ID 的非致命
// metadata rejection；空数组是唯一合法的清空快照。
func parseMetadataSnapshot(frame Frame) (map[string]int, error) {
	if frame.Type != FrameMetadataSnapshot {
		return nil, fmt.Errorf("frame type must be metadata_snapshot, got %q", frame.Type)
	}
	if frame.ID == "" {
		return nil, fmt.Errorf("metadata snapshot id must not be empty")
	}
	var entries []MetadataModel
	if len(frame.Body) > 0 {
		if err := json.Unmarshal(frame.Body, &entries); err != nil {
			return nil, fmt.Errorf("parse metadata snapshot body: %w", err)
		}
	}
	if len(entries) > MaxMetadataSnapshotEntries {
		return nil, fmt.Errorf("metadata snapshot has %d entries, max %d", len(entries), MaxMetadataSnapshotEntries)
	}
	out := make(map[string]int, len(entries))
	for _, e := range entries {
		if e.Model == "" {
			return nil, fmt.Errorf("metadata snapshot contains an empty model name")
		}
		if len([]byte(e.Model)) > MaxMetadataModelNameBytes {
			return nil, fmt.Errorf("metadata model name exceeds %d bytes", MaxMetadataModelNameBytes)
		}
		if _, dup := out[e.Model]; dup {
			return nil, fmt.Errorf("metadata snapshot contains duplicate model %q", e.Model)
		}
		if e.ContextLength == nil {
			out[e.Model] = 0
			continue
		}
		if *e.ContextLength <= 0 {
			return nil, fmt.Errorf("metadata model %q context_length must be positive, got %d", e.Model, *e.ContextLength)
		}
		out[e.Model] = *e.ContextLength
	}
	return out, nil
}
