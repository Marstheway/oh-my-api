package handler

import (
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/gin-gonic/gin"
)

// contextLengthLookup 获取当前生效的 context_length 查找函数。
// 优先级：
//  1. 活跃 Cascade session 元数据精确值（仅命中 Hub 已配置的 cascade provider 叶子）
//  2. 共享 catalog source（probe 精确值 → models.dev 回退）
//  3. 从文件加载的索引（兼容路径）
func contextLengthLookup() func(provider, upstreamModel string) (int, bool) {
	catalogLookup := func(provider, upstreamModel string) (int, bool) {
		if catalogSrc != nil {
			return catalogSrc.ContextLength(provider, upstreamModel)
		}
		if catalogIdx == nil {
			return 0, false
		}
		return catalogIdx.LookupContextLength(provider, upstreamModel)
	}

	if cascadeHubs == nil {
		return catalogLookup
	}
	return func(provider, upstreamModel string) (int, bool) {
		if v, ok := cascadeHubs.ContextLength(provider, upstreamModel); ok {
			return v, true
		}
		return catalogLookup(provider, upstreamModel)
	}
}

func Models(c *gin.Context) {
	userModels := resolver.ListUserModels()

	models := make([]dto.ModelInfo, len(userModels))
	lookup := contextLengthLookup()
	for i, m := range userModels {
		models[i] = dto.ModelInfo{
			ID:            m,
			Object:        "model",
			ContextLength: ResolveContextLength(m, resolver, lookup),
		}
	}

	c.JSON(http.StatusOK, dto.ModelListResponse{
		Data: models,
	})
}
