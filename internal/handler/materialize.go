package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/adaptor"
	"github.com/Marstheway/oh-my-api/internal/cascade"
	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/model"
	"github.com/Marstheway/oh-my-api/internal/rules"
	"github.com/Marstheway/oh-my-api/internal/scheduler"
)

// materializeInput carries shared inputs for plan materialization.
type materializeInput struct {
	InboundFormat codec.Format
	InboundCodec  codec.Codec
	RawReq        any
	ModelGroup    string
	ClientModel   string
	KeyName       string
	Rules         []config.RuleConfig
	CascadePeer   string
	CascadeHop    string
}

// materializePlan 将 model.PlanNode 递归转换为可执行的 scheduler.RunNode。
func materializePlan(ctx context.Context, node *model.PlanNode, in materializeInput) (*scheduler.RunNode, error) {
	if node == nil {
		return nil, fmt.Errorf("nil plan node")
	}

	leaves, err := materializeLeaves(ctx, node.Leaves, in)
	if err != nil {
		return nil, err
	}

	children, err := materializeChildren(ctx, node.Children, in)
	if err != nil {
		return nil, err
	}

	allChildren := mergeByOrder(node.ModelOrder, leaves, children)

	return &scheduler.RunNode{
		IsLeaf:   false,
		Name:     node.GroupName,
		Mode:     node.Mode,
		Weight:   node.Weight,
		Sticky:   node.Sticky,
		Children: allChildren,
	}, nil
}

// winnerOutboundFormat 返回 winner 物化时选中的出站协议，不重算。
func winnerOutboundFormat(resp *scheduler.Result, inboundFormat codec.Format) (codec.Format, string, int) {
	if resp != nil && resp.OutboundProtocol != "" {
		if f, err := codec.NormalizeProviderFormat(resp.OutboundProtocol); err == nil {
			return f, "materialized", 0
		}
	}
	return inboundFormat, "unknown", 0
}

func mergeByOrder(order []model.OrderEntry, leaves []*scheduler.RunNode, children []*scheduler.RunNode) []*scheduler.RunNode {
	if len(order) == 0 {
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

func materializeChildren(ctx context.Context, children []*model.PlanNode, in materializeInput) ([]*scheduler.RunNode, error) {
	nodes := make([]*scheduler.RunNode, 0, len(children))
	for _, child := range children {
		n, err := materializePlan(ctx, child, in)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func materializeLeaves(ctx context.Context, leaves []model.PlanLeaf, in materializeInput) ([]*scheduler.RunNode, error) {
	nodes := make([]*scheduler.RunNode, 0, len(leaves))
	for _, leaf := range leaves {
		n, err := materializeLeaf(ctx, leaf, in)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func materializeLeaf(ctx context.Context, leaf model.PlanLeaf, in materializeInput) (*scheduler.RunNode, error) {
	if shouldSkipCascadeLeaf(ctx, leaf, in.CascadePeer) {
		task := scheduler.Task{
			ProviderName:  leaf.ProviderName,
			UpstreamModel: leaf.UpstreamModel,
			ModelGroup:    in.ModelGroup,
			Stream:        requestIsStream(in.RawReq),
		}
		return &scheduler.RunNode{
			IsLeaf:        true,
			Unschedulable: true,
			Task:          task,
		}, nil
	}

	matchCtx := rules.MatchContext{
		ClientModel:   in.ClientModel,
		KeyName:       in.KeyName,
		UpstreamModel: leaf.ProviderName + "/" + leaf.UpstreamModel,
	}
	merged := rules.Evaluate(in.Rules, matchCtx)

	outboundFormat, schedulable, selErr := selectLeafOutboundFormat(leaf, in.InboundFormat, merged)
	if selErr != nil {
		return nil, selErr
	}

	task := scheduler.Task{
		ProviderName:     leaf.ProviderName,
		Provider:         leaf.Provider,
		UpstreamModel:    leaf.UpstreamModel,
		ModelGroup:       in.ModelGroup,
		OutboundProtocol: string(outboundFormat),
		Weight:           leaf.Weight,
		Stream:           requestIsStream(in.RawReq),
	}
	// 调度类 action（qpm / 时段 / retries）写入 Task，由调度器在执行时扣桶/跳过/重试。
	// 时段不在物化时按当前时钟钉死：protocol 不可达才用 Unschedulable。
	if merged.QPM != nil {
		task.ModelQPM = *merged.QPM
	}
	if merged.Retries != nil {
		task.Retries = *merged.Retries
	}
	task.EnableTimeRange = append([]string(nil), merged.EnableTimeRange...)
	task.DisableTimeRange = append([]string(nil), merged.DisableTimeRange...)

	if !schedulable {
		slog.Debug("leaf marked unschedulable by rules",
			"provider", leaf.ProviderName,
			"upstream_model", leaf.UpstreamModel,
			"forced_protocol", merged.Protocol,
			"outbound_format", string(outboundFormat),
		)
		return &scheduler.RunNode{
			IsLeaf:        true,
			Unschedulable: true,
			Task:          task,
		}, nil
	}

	processedReq := applyLeafRules(in.RawReq, merged)

	compat := codec.NeedsDeepSeekCompat(leaf.UpstreamModel)
	bodyBytes, encErr := in.InboundCodec.EncodeRequest(outboundFormat, processedReq, leaf.UpstreamModel, compat)
	if encErr != nil {
		return nil, encErr
	}

	matAttrs := []any{
		"provider", leaf.ProviderName,
		"model_group", in.ModelGroup,
		"inbound_format", string(in.InboundFormat),
		"outbound_format", string(outboundFormat),
		"upstream_model", leaf.UpstreamModel,
		"deepseek_compat", compat,
	}
	matAttrs = append(matAttrs, scheduler.ParseRequestBodyDiagAttrs(bodyBytes)...)
	slog.Debug("materialized upstream request", matAttrs...)

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

	capturedLeaf := leaf
	capturedAda := ada
	capturedProtocol := adaProtocol
	capturedBody := bodyBytes

	requestFactory := func() (*http.Request, error) {
		req := capturedAda.BuildRequest(ctx, &capturedLeaf.Provider, capturedLeaf.UpstreamModel, bytes.NewReader(capturedBody), capturedProtocol)
		if cascade.CascadeOrigin(ctx) {
			hop, _ := cascade.CascadeHop(ctx)
			if hop == "" {
				hop = in.CascadeHop
			}
			if hop != "" {
				req.Header.Set(cascade.HopHeader, hop)
			}
		}
		bodyCopy := capturedBody
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyCopy)), nil
		}
		req.ContentLength = int64(len(bodyCopy))
		return req, nil
	}

	return &scheduler.RunNode{
		IsLeaf:         true,
		Task:           task,
		RequestFactory: requestFactory,
	}, nil
}

func selectLeafOutboundFormat(leaf model.PlanLeaf, inboundFormat codec.Format, merged rules.MergedAction) (codec.Format, bool, error) {
	if merged.Protocol != "" {
		forced, err := codec.NormalizeProviderFormat(merged.Protocol)
		if err != nil {
			// Invalid protocols must fail Load/Apply; treat residual cases as hard errors.
			return "", false, fmt.Errorf("invalid forced protocol %q for %s/%s: %w", merged.Protocol, leaf.ProviderName, leaf.UpstreamModel, err)
		}
		if !leaf.Provider.ProtocolReachable(merged.Protocol) {
			return forced, false, nil
		}
		return forced, true, nil
	}

	outboundFormat, _, _, err := leaf.Provider.SelectOutboundFormat(inboundFormat)
	if err != nil {
		return "", false, fmt.Errorf("select outbound format for %s/%s: %w", leaf.ProviderName, leaf.UpstreamModel, err)
	}
	return outboundFormat, true, nil
}

func requestIsStream(rawReq any) bool {
	switch r := rawReq.(type) {
	case *dto.ChatCompletionRequest:
		return r.Stream
	case *dto.ClaudeRequest:
		return r.Stream
	case *dto.ResponsesRequest:
		return r.Stream
	case *dto.OllamaChatRequest:
		return r.Stream
	default:
		return false
	}
}

func shouldSkipCascadeLeaf(ctx context.Context, leaf model.PlanLeaf, cascadePeer string) bool {
	if cascade.CascadeOrigin(ctx) && cascadePeer != "" && leaf.ProviderName == cascadePeer {
		return true
	}
	if hop, ok := cascade.CascadeHop(ctx); ok {
		if leaf.Provider.Cascade != nil && leaf.Provider.Cascade.Enabled &&
			cascade.TokenMatches(hop, leaf.Provider.Cascade.Token) {
			slog.Debug("leaf marked unschedulable by cascade hop",
				"provider", leaf.ProviderName,
			)
			return true
		}
	}
	return false
}
