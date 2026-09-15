package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/codec"
	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/Marstheway/oh-my-api/internal/model"
)

// TestMaterializePlan_PreservesModelOrder_Failover 验证 failover 模式下
// materializePlan 按配置顺序合并叶子与子 group，确保子 group 调度器不被跳过。
func TestMaterializePlan_PreservesModelOrder_Failover(t *testing.T) {
	// 配置：child-group (adaptive) 在前，两个叶子在后
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"prov-success": {
				Endpoint:  "https://success.example.com",
				APIKey:    "key-success",
				Protocols: []string{"openai.chat"},
				RateLimit: config.RateLimitConfig{QPM: 0},
			},
			"prov-fail": {
				Endpoint:  "https://fail.example.com",
				APIKey:    "key-fail",
				Protocols: []string{"openai.chat"},
				RateLimit: config.RateLimitConfig{QPM: 0},
			},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name: "child-group",
				Mode: "adaptive",
				Models: config.ModelEntries{
					{Model: "prov-fail/model-fail", Weight: 1}, // 会失败
				},
			},
			{
				Name: "root",
				Mode: "failover",
				Models: config.ModelEntries{
					{Model: "child-group", Weight: 1},                // 子 group 在前
					{Model: "prov-success/model-success", Weight: 1}, // 叶子在后
				},
			},
		},
	}

	r, err := model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver failed: %v", err)
	}

	plan, err := r.BuildPlanNodeForTest("root")
	if err != nil {
		t.Fatalf("BuildPlanNodeForTest failed: %v", err)
	}

	// Mock server: child-group 的 provider 会失败，root 的叶子会成功
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "internal error"}`))
	}))
	defer failSrv.Close()

	successSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"success","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer successSrv.Close()

	// 修改 provider endpoint 为 mock server
	failProv := cfg.Providers.Items["prov-fail"]
	failProv.Endpoint = failSrv.URL
	cfg.Providers.Items["prov-fail"] = failProv

	successProv := cfg.Providers.Items["prov-success"]
	successProv.Endpoint = successSrv.URL
	cfg.Providers.Items["prov-success"] = successProv

	// Materialize
	inboundCodec, err := codec.Get(codec.FormatOpenAIChat)
	if err != nil {
		t.Fatalf("codec.Get failed: %v", err)
	}

	runNode, err := materializePlan(context.Background(), plan, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        &dto.ChatCompletionRequest{Model: "root", Messages: []dto.Message{}},
		ModelGroup:    "root",
		ClientModel:   "root",
		KeyName:       "",
		Rules:         nil,
	})
	if err != nil {
		t.Fatalf("materializePlan failed: %v", err)
	}

	// 验证 Children 顺序：期望 [child-group, prov-success/model-success]
	if len(runNode.Children) != 2 {
		t.Fatalf("Children count = %d, want 2", len(runNode.Children))
	}

	// 第一个应该是子 group（adaptive）
	first := runNode.Children[0]
	if first.IsLeaf {
		t.Error("first child should be group node (IsLeaf=false), got leaf")
	}
	if first.Name != "child-group" {
		t.Errorf("first child name = %s, want child-group", first.Name)
	}
	if first.Mode != "adaptive" {
		t.Errorf("first child mode = %s, want adaptive", first.Mode)
	}

	// 第二个应该是叶子
	second := runNode.Children[1]
	if !second.IsLeaf {
		t.Error("second child should be leaf node (IsLeaf=true), got group")
	}
	if second.Task.ProviderName != "prov-success" {
		t.Errorf("second child provider = %s, want prov-success", second.Task.ProviderName)
	}
}

// TestMaterializePlan_PreservesModelOrder_Concurrent 验证 concurrent 模式下
// 配置顺序正确保留（虽然 concurrent 不依赖顺序，但仍应按配置构建）。
func TestMaterializePlan_PreservesModelOrder_Concurrent(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"prov-a": {Endpoint: "https://a.example.com", APIKey: "key-a", Protocols: []string{"openai.chat"}},
			"prov-b": {Endpoint: "https://b.example.com", APIKey: "key-b", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name: "child",
				Mode: "concurrent",
				Models: config.ModelEntries{
					{Model: "prov-a/model-a", Weight: 1},
				},
			},
			{
				Name: "root",
				Mode: "concurrent",
				Models: config.ModelEntries{
					{Model: "child", Weight: 1},          // 子 group
					{Model: "prov-b/model-b", Weight: 1}, // 叶子
				},
			},
		},
	}

	r, err := model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver failed: %v", err)
	}

	plan, err := r.BuildPlanNodeForTest("root")
	if err != nil {
		t.Fatalf("BuildPlanNodeForTest failed: %v", err)
	}

	inboundCodec, err := codec.Get(codec.FormatOpenAIChat)
	if err != nil {
		t.Fatalf("codec.Get failed: %v", err)
	}

	runNode, err := materializePlan(context.Background(), plan, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        &dto.ChatCompletionRequest{Model: "root", Messages: []dto.Message{}},
		ModelGroup:    "root",
		ClientModel:   "root",
		KeyName:       "",
		Rules:         nil,
	})
	if err != nil {
		t.Fatalf("materializePlan failed: %v", err)
	}

	// 验证顺序：[child, prov-b]
	if len(runNode.Children) != 2 {
		t.Fatalf("Children count = %d, want 2", len(runNode.Children))
	}

	if runNode.Children[0].IsLeaf {
		t.Error("first child should be group, got leaf")
	}
	if runNode.Children[1].Task.ProviderName != "prov-b" {
		t.Errorf("second child provider = %s, want prov-b", runNode.Children[1].Task.ProviderName)
	}
}

// TestMaterializePlan_PreservesModelOrder_LoadBalance 验证 load-balance 模式下
// 配置顺序正确保留（虽然 load-balance 按权重随机选择，但仍应按配置构建）。
func TestMaterializePlan_PreservesModelOrder_LoadBalance(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"prov-a": {Endpoint: "https://a.example.com", APIKey: "key-a", Protocols: []string{"openai.chat"}},
			"prov-b": {Endpoint: "https://b.example.com", APIKey: "key-b", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name: "child",
				Mode: "failover",
				Models: config.ModelEntries{
					{Model: "prov-a/model-a", Weight: 1},
				},
			},
			{
				Name: "root",
				Mode: "load-balance",
				Models: config.ModelEntries{
					{Model: "child", Weight: 2},          // 子 group，权重 2
					{Model: "prov-b/model-b", Weight: 1}, // 叶子，权重 1
				},
			},
		},
	}

	r, err := model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver failed: %v", err)
	}

	plan, err := r.BuildPlanNodeForTest("root")
	if err != nil {
		t.Fatalf("BuildPlanNodeForTest failed: %v", err)
	}

	inboundCodec, err := codec.Get(codec.FormatOpenAIChat)
	if err != nil {
		t.Fatalf("codec.Get failed: %v", err)
	}

	runNode, err := materializePlan(context.Background(), plan, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        &dto.ChatCompletionRequest{Model: "root", Messages: []dto.Message{}},
		ModelGroup:    "root",
		ClientModel:   "root",
		KeyName:       "",
		Rules:         nil,
	})
	if err != nil {
		t.Fatalf("materializePlan failed: %v", err)
	}

	// 验证顺序：[child, prov-b]
	if len(runNode.Children) != 2 {
		t.Fatalf("Children count = %d, want 2", len(runNode.Children))
	}

	if runNode.Children[0].IsLeaf {
		t.Error("first child should be group, got leaf")
	}
	if runNode.Children[0].Weight != 2 {
		t.Errorf("first child weight = %d, want 2", runNode.Children[0].Weight)
	}
	if runNode.Children[1].Task.ProviderName != "prov-b" {
		t.Errorf("second child provider = %s, want prov-b", runNode.Children[1].Task.ProviderName)
	}
}

// TestMaterializePlan_PreservesModelOrder_Adaptive 验证 adaptive 模式下
// 配置顺序正确保留（adaptive 会按 TTFT score 重排叶子，但子 group 应保持原位置）。
func TestMaterializePlan_PreservesModelOrder_Adaptive(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"prov-a": {Endpoint: "https://a.example.com", APIKey: "key-a", Protocols: []string{"openai.chat"}},
			"prov-b": {Endpoint: "https://b.example.com", APIKey: "key-b", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name: "child",
				Mode: "concurrent",
				Models: config.ModelEntries{
					{Model: "prov-a/model-a", Weight: 1},
				},
			},
			{
				Name: "root",
				Mode: "adaptive",
				Models: config.ModelEntries{
					{Model: "child", Weight: 1},          // 子 group
					{Model: "prov-b/model-b", Weight: 1}, // 叶子
				},
			},
		},
	}

	r, err := model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver failed: %v", err)
	}

	plan, err := r.BuildPlanNodeForTest("root")
	if err != nil {
		t.Fatalf("BuildPlanNodeForTest failed: %v", err)
	}

	inboundCodec, err := codec.Get(codec.FormatOpenAIChat)
	if err != nil {
		t.Fatalf("codec.Get failed: %v", err)
	}

	runNode, err := materializePlan(context.Background(), plan, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        &dto.ChatCompletionRequest{Model: "root", Messages: []dto.Message{}},
		ModelGroup:    "root",
		ClientModel:   "root",
		KeyName:       "",
		Rules:         nil,
	})
	if err != nil {
		t.Fatalf("materializePlan failed: %v", err)
	}

	// 验证顺序：[child, prov-b]
	// 虽然 adaptive 在执行时会按 score 排序叶子，但 materializePlan 输出的结构应按配置顺序
	if len(runNode.Children) != 2 {
		t.Fatalf("Children count = %d, want 2", len(runNode.Children))
	}

	if runNode.Children[0].IsLeaf {
		t.Error("first child should be group, got leaf")
	}
	if runNode.Children[1].Task.ProviderName != "prov-b" {
		t.Errorf("second child provider = %s, want prov-b", runNode.Children[1].Task.ProviderName)
	}
}

// TestMaterializePlan_PreservesModelOrder_PureChildGroups 验证纯子 group
// 场景（无直接叶子）下 mergeByOrder 正确处理 children 路径。
func TestMaterializePlan_PreservesModelOrder_PureChildGroups(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{Items: map[string]config.ProviderConfig{
			"prov-a": {Endpoint: "https://a.example.com", APIKey: "key-a", Protocols: []string{"openai.chat"}},
			"prov-b": {Endpoint: "https://b.example.com", APIKey: "key-b", Protocols: []string{"openai.chat"}},
		}},
		ModelGroups: []config.ModelGroupConfig{
			{
				Name: "child-a",
				Mode: "concurrent",
				Models: config.ModelEntries{
					{Model: "prov-a/model-a", Weight: 1},
				},
			},
			{
				Name: "child-b",
				Mode: "failover",
				Models: config.ModelEntries{
					{Model: "prov-b/model-b", Weight: 1},
				},
			},
			{
				Name: "root",
				Mode: "failover",
				Models: config.ModelEntries{
					{Model: "child-a", Weight: 1}, // 纯子 group，无直接叶子
					{Model: "child-b", Weight: 2},
				},
			},
		},
	}

	r, err := model.NewResolver(cfg)
	if err != nil {
		t.Fatalf("NewResolver failed: %v", err)
	}

	plan, err := r.BuildPlanNodeForTest("root")
	if err != nil {
		t.Fatalf("BuildPlanNodeForTest failed: %v", err)
	}

	// 验证 PlanNode：无叶子，两个子 group
	if len(plan.Leaves) != 0 {
		t.Errorf("Leaves count = %d, want 0 (pure child group)", len(plan.Leaves))
	}
	if len(plan.Children) != 2 {
		t.Errorf("Children count = %d, want 2", len(plan.Children))
	}
	if len(plan.ModelOrder) != 2 {
		t.Errorf("ModelOrder length = %d, want 2", len(plan.ModelOrder))
	}

	inboundCodec, err := codec.Get(codec.FormatOpenAIChat)
	if err != nil {
		t.Fatalf("codec.Get failed: %v", err)
	}

	runNode, err := materializePlan(context.Background(), plan, materializeInput{
		InboundFormat: codec.FormatOpenAIChat,
		InboundCodec:  inboundCodec,
		RawReq:        &dto.ChatCompletionRequest{Model: "root", Messages: []dto.Message{}},
		ModelGroup:    "root",
		ClientModel:   "root",
		KeyName:       "",
		Rules:         nil,
	})
	if err != nil {
		t.Fatalf("materializePlan failed: %v", err)
	}

	// 验证顺序：[child-a, child-b]
	if len(runNode.Children) != 2 {
		t.Fatalf("Children count = %d, want 2", len(runNode.Children))
	}

	if runNode.Children[0].IsLeaf {
		t.Error("first child should be group, got leaf")
	}
	if runNode.Children[0].Name != "child-a" {
		t.Errorf("first child name = %s, want child-a", runNode.Children[0].Name)
	}

	if runNode.Children[1].IsLeaf {
		t.Error("second child should be group, got leaf")
	}
	if runNode.Children[1].Name != "child-b" {
		t.Errorf("second child name = %s, want child-b", runNode.Children[1].Name)
	}
}
