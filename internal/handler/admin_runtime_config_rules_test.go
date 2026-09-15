package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Marstheway/oh-my-api/internal/config"
	"github.com/Marstheway/oh-my-api/internal/runtimeconfig"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rulesResponse struct {
	Rules []runtimeconfig.RuleOutput `json:"rules"`
}

func TestAdminRuntimeConfigHandler_GetRules(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.GET("/admin/runtime-config/draft/rules", h.GetRules)
	r.PUT("/admin/runtime-config/draft/rules", h.ReplaceRules)

	// 初始为空表
	req := httptest.NewRequest(http.MethodGet, "/admin/runtime-config/draft/rules", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp rulesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp.Rules, 0)

	// 写入两条规则（含全局规则）后 GET 顺序与 draft 一致
	putBody := `{"rules":[
		{"match":{"client_model":{"op":"equals","value":"gpt-4"}},"action":{"protocol":"openai.chat"}},
		{"match":{},"action":{"effort":["high","max"],"temperature_mode":"strip"}}
	]}`
	req = httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/rules", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest(http.MethodGet, "/admin/runtime-config/draft/rules", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Rules, 2)
	assert.Equal(t, "equals", resp.Rules[0].Match.ClientModel.Op)
	assert.Equal(t, "gpt-4", resp.Rules[0].Match.ClientModel.Value)
	assert.Nil(t, resp.Rules[0].Match.Key)
	assert.Nil(t, resp.Rules[0].Match.UpstreamModel)
	assert.Equal(t, "openai.chat", resp.Rules[0].Action.Protocol)
	assert.Nil(t, resp.Rules[1].Match.ClientModel)
	assert.Equal(t, []string{"high", "max"}, resp.Rules[1].Action.Effort)
	assert.Equal(t, "strip", resp.Rules[1].Action.TemperatureMode)
}

func TestAdminRuntimeConfigHandler_ReplaceRulesBodyContract(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.PUT("/admin/runtime-config/draft/rules", h.ReplaceRules)

	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/rules", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 合法整表替换；协议别名规范化后返回
	w := put(`{"rules":[{"action":{"protocol":"openai"}}]}`)
	assert.Equal(t, http.StatusOK, w.Code)
	var resp rulesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Rules, 1)
	assert.Equal(t, "openai.chat", resp.Rules[0].Action.Protocol)

	// 缺 rules 键 → 400，draft 不变
	w = put(`{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Len(t, mgr.ListRules(), 1)

	// rules: null → 400，draft 不变
	w = put(`{"rules":null}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Len(t, mgr.ListRules(), 1)

	// rules: [] → 200 清空
	w = put(`{"rules":[]}`)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Len(t, mgr.ListRules(), 0)

	// 非法 JSON → 400
	w = put(`not-json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAdminRuntimeConfigHandler_ReplaceRulesValidation(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.PUT("/admin/runtime-config/draft/rules", h.ReplaceRules)

	// 先写入一条合法规则
	putBody := `{"rules":[{"action":{"protocol":"openai.chat"}}]}`
	req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/rules", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	before := mgr.ListRules()

	// 非法 op → 422，错误带 rules[i] 路径，draft 不变
	putBody = `{"rules":[{"match":{"key":{"op":"regex","value":"x"}}}]}`
	req = httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/rules", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)

	var errResp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Field   string `json:"field"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Equal(t, "validation_error", errResp.Error.Code)
	assert.Contains(t, errResp.Error.Message, "rules[0].match.key.op")
	assert.Equal(t, "rules[0].match.key.op", errResp.Error.Field)
	assert.Len(t, mgr.ListRules(), len(before))

	// effort 与 effort_mode 同条互斥 → 422
	putBody = `{"rules":[{"action":{"effort":["high"],"effort_mode":"strip"}}]}`
	req = httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/rules", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Len(t, mgr.ListRules(), len(before))

	// 空 action → 422，错误路径 rules[0].action，draft 不变
	putBody = `{"rules":[{"match":{"key":{"op":"equals","value":"x"}},"action":{}}]}`
	req = httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/rules", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Equal(t, "validation_error", errResp.Error.Code)
	assert.Contains(t, errResp.Error.Message, "rules[0].action")
	assert.Equal(t, "rules[0].action", errResp.Error.Field)
	assert.Len(t, mgr.ListRules(), len(before))

	// 同条双 match 字段 → 422，错误路径 rules[0].match，draft 不变
	putBody = `{"rules":[{"match":{"client_model":{"op":"equals","value":"gpt-4"},"key":{"op":"equals","value":"cc-"}},"action":{"thinking":"off"}}]}`
	req = httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/rules", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Equal(t, "rules[0].match", errResp.Error.Field)
	assert.Len(t, mgr.ListRules(), len(before))

	// effort none 与其它档位混用 → 422
	putBody = `{"rules":[{"action":{"effort":["none","high"]}}]}`
	req = httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/rules", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Len(t, mgr.ListRules(), len(before))

	// 同条多类 action（protocol + thinking + qpm）合法 → 200
	putBody = `{"rules":[{"match":{"upstream_model":{"op":"equals","value":"openai/gpt-4o"}},"action":{"protocol":"openai.chat","thinking":"off","qpm":120}}]}`
	req = httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/rules", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	require.Len(t, mgr.ListRules(), 1)
	got := mgr.ListRules()[0]
	assert.Equal(t, "openai.chat", got.Action.Protocol)
	assert.Equal(t, "off", got.Action.Thinking)
	require.NotNil(t, got.Action.QPM)
	assert.Equal(t, 120, *got.Action.QPM)

	// effort [none] 独占合法 → 200
	putBody = `{"rules":[{"action":{"effort":["none"]}}]}`
	req = httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/rules", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Len(t, mgr.ListRules(), 1)
	assert.Equal(t, []string{"none"}, mgr.ListRules()[0].Action.Effort)
}

// TestAdminRuntimeConfigHandler_ReplaceRulesSchedulingActions 覆盖三类调度 action 的 PUT 合同：
// 合法 qpm（仅 upstream-model match）与两类时段 round-trip 成功；qpm 配 GLOBAL/client-model/key、
// 显式 qpm:0、非法/空时段均 422 且错误带 rules[i] 路径、draft 不变。
func TestAdminRuntimeConfigHandler_ReplaceRulesSchedulingActions(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr := createInMemoryManager(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.PUT("/admin/runtime-config/draft/rules", h.ReplaceRules)
	r.GET("/admin/runtime-config/draft/rules", h.GetRules)

	// 先写入一条合法规则
	putBody := `{"rules":[{"action":{"protocol":"openai.chat"}}]}`
	req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/rules", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	before := mgr.ListRules()

	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/rules", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 合法 qpm + upstream-model → 200，round-trip 保真
	w = put(`{"rules":[{"match":{"upstream_model":{"op":"equals","value":"openai/gpt-4o"}},"action":{"qpm":120}}]}`)
	assert.Equal(t, http.StatusOK, w.Code)
	var resp rulesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Rules, 1)
	require.NotNil(t, resp.Rules[0].Action.QPM)
	assert.Equal(t, 120, *resp.Rules[0].Action.QPM)

	// 合法 enable/disable_time_range → 200，round-trip 保真
	w = put(`{"rules":[
		{"match":{"upstream_model":{"op":"startWith","value":"openrouter/"}},"action":{"enable_time_range":["09:00-18:00","23:00-02:00"]}},
		{"match":{"key":{"op":"equals","value":"sk-test"}},"action":{"disable_time_range":["23:00-02:00"]}}
	]}`)
	assert.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Rules, 2)
	assert.Equal(t, []string{"09:00-18:00", "23:00-02:00"}, resp.Rules[0].Action.EnableTimeRange)
	assert.Equal(t, []string{"23:00-02:00"}, resp.Rules[1].Action.DisableTimeRange)
	before = mgr.ListRules()

	// qpm + GLOBAL → 422 rules[0].action.qpm，draft 不变
	w = put(`{"rules":[{"action":{"qpm":100}}]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	var errResp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Field   string `json:"field"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Equal(t, "validation_error", errResp.Error.Code)
	assert.Contains(t, errResp.Error.Message, "rules[0].action.qpm")
	assert.Equal(t, "rules[0].action.qpm", errResp.Error.Field)
	assert.Len(t, mgr.ListRules(), len(before))

	// qpm + client-model match → 422 rules[0].action.qpm
	w = put(`{"rules":[{"match":{"client_model":{"op":"equals","value":"gpt-4"}},"action":{"qpm":100}}]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Contains(t, errResp.Error.Message, "rules[0].action.qpm")
	assert.Len(t, mgr.ListRules(), len(before))

	// qpm + key match → 422 rules[0].action.qpm
	w = put(`{"rules":[{"match":{"key":{"op":"equals","value":"cc-"}},"action":{"qpm":100}}]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Contains(t, errResp.Error.Message, "rules[0].action.qpm")
	assert.Len(t, mgr.ListRules(), len(before))

	// 显式 qpm: 0 → 422 rules[0].action.qpm（不得当作未设置）
	w = put(`{"rules":[{"match":{"upstream_model":{"op":"equals","value":"openai/gpt-4o"}},"action":{"qpm":0}}]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Equal(t, "rules[0].action.qpm", errResp.Error.Field)
	assert.Len(t, mgr.ListRules(), len(before))

	// 非法时段字符串 → 422 rules[0].action.enable_time_range[0]
	w = put(`{"rules":[{"match":{"upstream_model":{"op":"equals","value":"openai/gpt-4o"}},"action":{"enable_time_range":["9am-5pm"]}}]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Contains(t, errResp.Error.Message, "rules[0].action.enable_time_range[0]")
	assert.Len(t, mgr.ListRules(), len(before))

	// 空时段列表 → 422 rules[0].action（空 action）
	w = put(`{"rules":[{"match":{"upstream_model":{"op":"equals","value":"openai/gpt-4o"}},"action":{"disable_time_range":[]}}]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Equal(t, "rules[0].action", errResp.Error.Field)
	assert.Len(t, mgr.ListRules(), len(before))

	// 合法 retries 含显式 0（覆盖）与 GLOBAL → 200 round-trip
	w = put(`{"rules":[{"action":{"retries":2}},{"match":{"upstream_model":{"op":"equals","value":"openai/gpt-4o"}},"action":{"retries":0}}]}`)
	assert.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Rules, 2)
	require.NotNil(t, resp.Rules[0].Action.Retries)
	assert.Equal(t, 2, *resp.Rules[0].Action.Retries)
	require.NotNil(t, resp.Rules[1].Action.Retries)
	assert.Equal(t, 0, *resp.Rules[1].Action.Retries)
	before = mgr.ListRules()

	// retries: 3 → 422 rules[0].action.retries，draft 不变
	w = put(`{"rules":[{"action":{"retries":3}}]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Equal(t, "rules[0].action.retries", errResp.Error.Field)
	assert.Len(t, mgr.ListRules(), len(before))
}

// TestAdminRuntimeConfigHandler_ReplaceRulesApplyPersists 覆盖 Apply 后 YAML 落盘：
// rules 顺序与内容与 draft 一致、YAML 键名为 client-model、其它段不丢；
// 只改 provider 再 Apply，rules 不丢不乱序。
func TestAdminRuntimeConfigHandler_ReplaceRulesApplyPersists(t *testing.T) {
	cfg := createBasicTestConfig()
	mgr, configPath := createInMemoryManagerWithPath(t, cfg)
	h := NewAdminRuntimeConfigHandler(mgr)

	r := gin.New()
	r.PUT("/admin/runtime-config/draft/rules", h.ReplaceRules)
	r.POST("/admin/runtime-config/apply", h.Apply)

	putBody := `{"rules":[
		{"match":{"client_model":{"op":"equals","value":"gpt-4"}},"action":{"protocol":"openai.chat"}},
		{"match":{"client_model":{"op":"equals","value":"gpt-4"}},"action":{"thinking":"off"}},
		{"match":{},"action":{"effort":["high","max"]}},
		{"match":{"upstream_model":{"op":"equals","value":"openai/gpt-4o"}},"action":{"qpm":120}}
	]}`
	req := httptest.NewRequest(http.MethodPut, "/admin/runtime-config/draft/rules", strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	req = httptest.NewRequest(http.MethodPost, "/admin/runtime-config/apply", strings.NewReader(`{"confirm":true}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// YAML 落盘：kebab-case 键名 + 调度 action 的 snake_case 键名
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "client-model")
	assert.NotContains(t, content, "client_model")
	assert.Contains(t, content, "qpm: 120")

	// 重新 Load：顺序与内容与 draft 一致，其它段不丢
	reloadCfg, err := config.Load(configPath)
	require.NoError(t, err)
	require.Len(t, reloadCfg.Rules, 4)
	r0 := reloadCfg.Rules[0]
	assert.Equal(t, "gpt-4", r0.Match.ClientModel.Value)
	assert.Nil(t, r0.Match.Key)
	assert.Nil(t, r0.Match.UpstreamModel)
	assert.Equal(t, "openai.chat", r0.Action.Protocol)
	r1 := reloadCfg.Rules[1]
	assert.Equal(t, "gpt-4", r1.Match.ClientModel.Value)
	assert.Equal(t, "off", r1.Action.Thinking)
	r2 := reloadCfg.Rules[2]
	assert.Nil(t, r2.Match.ClientModel)
	assert.Equal(t, []string{"high", "max"}, r2.Action.Effort)
	r3 := reloadCfg.Rules[3]
	require.NotNil(t, r3.Match.UpstreamModel)
	assert.Equal(t, "openai/gpt-4o", r3.Match.UpstreamModel.Value)
	require.NotNil(t, r3.Action.QPM)
	assert.Equal(t, 120, *r3.Action.QPM)
	assert.Len(t, reloadCfg.Providers.Items, 1)
	assert.Len(t, reloadCfg.ModelGroups, 2)
	assert.Len(t, reloadCfg.Inbound.Auth.Keys, 1)

	// 只改 provider 再 Apply，rules 不丢不乱序
	err = mgr.UpdateProvider("openai", &runtimeconfig.ProviderInput{
		Name:      "openai",
		Endpoint:  "https://api.openai.com/v2",
		APIKey:    "sk-openai",
		Protocols: []string{"openai.chat"},
	})
	require.NoError(t, err)

	req = httptest.NewRequest(http.MethodPost, "/admin/runtime-config/apply", strings.NewReader(`{"confirm":true}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	reloadCfg, err = config.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com/v2", reloadCfg.Providers.Items["openai"].Endpoint)
	require.Len(t, reloadCfg.Rules, 4)
	assert.Equal(t, "gpt-4", reloadCfg.Rules[0].Match.ClientModel.Value)
	assert.Equal(t, "off", reloadCfg.Rules[1].Action.Thinking)
	assert.Equal(t, []string{"high", "max"}, reloadCfg.Rules[2].Action.Effort)
	require.NotNil(t, reloadCfg.Rules[3].Action.QPM)
	assert.Equal(t, 120, *reloadCfg.Rules[3].Action.QPM)
}
