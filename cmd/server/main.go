// Command server 启动车轴复核 HTTP API。
package main

import (
	"log"
	"os"

	"github.com/gin-gonic/gin"

	"axleverify/internal/httpapi"
)

func main() {
	port := os.Getenv("API_PORT")
	if port == "" {
		port = "8080"
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	httpapi.Register(r)

	log.Printf("车轴复核 API 监听 :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}
