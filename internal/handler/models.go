package handler

import (
	"net/http"

	"github.com/Marstheway/oh-my-api/internal/dto"
	"github.com/gin-gonic/gin"
)

func Models(c *gin.Context) {
	userModels := resolver.ListUserModels()

	models := make([]dto.ModelInfo, len(userModels))
	for i, m := range userModels {
		models[i] = dto.ModelInfo{
			ID:            m,
			Object:        "model",
			ContextLength: ResolveContextLength(m, resolver, catalogIdx),
		}
	}

	c.JSON(http.StatusOK, dto.ModelListResponse{
		Data: models,
	})
}
