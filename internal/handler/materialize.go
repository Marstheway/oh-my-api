package handler

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/adaptor"
	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
)

// materializePlan 将 model.PlanNode 递归转换为可执行的 scheduler.RunNode。
// inboundFormat 决定出方向编解码选择。
// inboundCodec 负责将请求体编码为目标协议格式。
// rawReq 为已解析的原始请求对象（如 *dto.ChatCompletionRequest）。
// modelGroup 为可观测性字段（在叶子 Task 中标记所在 group）。
func materializePlan(
	ctx context.Context,
	node *model.PlanNode,
	inboundFormat codec.Format,
	inboundCodec codec.Codec,
	rawReq any,
	modelGroup string,
) (*scheduler.RunNode, error) {
	if node == nil {
		return nil, fmt.Errorf("nil plan node")
	}

	leaves, err := materializeLeaves(ctx, node.Leaves, inboundFormat, inboundCodec, rawReq, modelGroup)
	if err != nil {
		return nil, err
	}

	children, err := materializeChildren(ctx, node.Children, inboundFormat, inboundCodec, rawReq, modelGroup)
	if err != nil {
		return nil, err
	}

	allChildren := mergeByOrder(node.ModelOrder, leaves, children)

	return &scheduler.RunNode{
		IsLeaf:   false,
		Name:     node.GroupName,
		Mode:     node.Mode,
		Weight:   node.Weight,
		Children: allChildren,
	}, nil
}

// mergeByOrder 按原始配置顺序合并叶子节点列表和子 group 节点列表。
func mergeByOrder(order []model.OrderEntry, leaves []*scheduler.RunNode, children []*scheduler.RunNode) []*scheduler.RunNode {
	if len(order) == 0 {
		// 兼容旧版本：order 为空时仍按 leaves + children 顺序（防止意外）
		return append(leaves, children...)
	}

	result := make([]*scheduler.RunNode, 0, len(order))
	for _, entry := range order {
		if entry.IsLeaf {
			if entry.Index < len(leaves) {
				result = append(result, leaves[entry.Index])
			}
		} else {
			if entry.Index < len(children) {
				result = append(result, children[entry.Index])
			}
		}
	}
	return result
}

// materializeChildren 批量物化子 group 节点。
func materializeChildren(
	ctx context.Context,
	children []*model.PlanNode,
	inboundFormat codec.Format,
	inboundCodec codec.Codec,
	rawReq any,
	modelGroup string,
) ([]*scheduler.RunNode, error) {
	nodes := make([]*scheduler.RunNode, 0, len(children))
	for _, child := range children {
		n, err := materializePlan(ctx, child, inboundFormat, inboundCodec, rawReq, modelGroup)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// materializeLeaves 批量物化叶子节点。
func materializeLeaves(
	ctx context.Context,
	leaves []model.PlanLeaf,
	inboundFormat codec.Format,
	inboundCodec codec.Codec,
	rawReq any,
	modelGroup string,
) ([]*scheduler.RunNode, error) {
	nodes := make([]*scheduler.RunNode, 0, len(leaves))
	for _, leaf := range leaves {
		n, err := materializeLeaf(ctx, leaf, inboundFormat, inboundCodec, rawReq, modelGroup)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// materializeLeaf 将单个叶子节点物化为带 RequestFactory 的 RunNode。
func materializeLeaf(
	ctx context.Context,
	leaf model.PlanLeaf,
	inboundFormat codec.Format,
	inboundCodec codec.Codec,
	rawReq any,
	modelGroup string,
) (*scheduler.RunNode, error) {
	outboundFormat, _, _, err := leaf.Provider.SelectOutboundFormatForModel(inboundFormat, leaf.UpstreamModel)
	if err != nil {
		return nil, fmt.Errorf("select outbound format for %s/%s: %w", leaf.ProviderName, leaf.UpstreamModel, err)
	}

	// 预编码请求体；RequestFactory 每次调用都会重新包装新的 Reader，但字节本身可复用
	// needsDeepSeekCompat 基于 upstream model 名称自动判断，无需配置
	compat := codec.NeedsDeepSeekCompat(leaf.UpstreamModel)
	bodyBytes, encErr := inboundCodec.EncodeRequest(outboundFormat, rawReq, leaf.UpstreamModel, compat)
	if encErr != nil {
		return nil, encErr
	}

	slog.Debug("materialized upstream request",
		"provider", leaf.ProviderName,
		"model_group", modelGroup,
		"inbound_format", string(inboundFormat),
		"outbound_format", string(outboundFormat),
		"upstream_model", leaf.UpstreamModel,
		"body_bytes", len(bodyBytes),
		"deepseek_compat", compat,
	)

	var ada adaptor.Adaptor
	var adaProtocol adaptor.Protocol
	switch outboundFormat {
	case codec.FormatAnthropicMessages:
		ada = adaptor.GetAdaptor(string(adaptor.ProtocolAnthropic))
		adaProtocol = adaptor.ProtocolAnthropic
	case codec.FormatOpenAIResponse:
		ada = adaptor.GetAdaptor(string(adaptor.ProtocolOpenAI))
		adaProtocol = adaptor.ProtocolOpenAIResponse
	case codec.FormatOllamaChat:
		ada = adaptor.GetAdaptor(string(adaptor.ProtocolOllamaChat))
		adaProtocol = adaptor.ProtocolOllamaChat
	default:
		ada = adaptor.GetAdaptor(string(adaptor.ProtocolOpenAI))
		adaProtocol = adaptor.ProtocolOpenAI
	}

	// 值拷贝，确保闭包捕获到独立副本
	capturedLeaf := leaf
	capturedAda := ada
	capturedProtocol := adaProtocol
	capturedBody := bodyBytes

	task := scheduler.Task{
		ProviderName:     leaf.ProviderName,
		Provider:         leaf.Provider,
		UpstreamModel:    leaf.UpstreamModel,
		ModelGroup:       modelGroup,
		OutboundProtocol: string(outboundFormat),
		Weight:           leaf.Weight,
	}

	requestFactory := func() (*http.Request, error) {
		req := capturedAda.BuildRequest(ctx, &capturedLeaf.Provider, capturedLeaf.UpstreamModel, bytes.NewReader(capturedBody), capturedProtocol)
		return req, nil
	}

	return &scheduler.RunNode{
		IsLeaf:         true,
		Task:           task,
		RequestFactory: requestFactory,
	}, nil
}
