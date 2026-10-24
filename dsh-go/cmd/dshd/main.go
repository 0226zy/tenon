// dshd 是 dsh-go 的服务端入口：装配组合根，启动 HTTP 服务。
//
//	dshd [-config conf/dsh.yaml]
//
// 形态对齐 `dsh web`：默认监听 127.0.0.1:3080，Web UI 在 /，API 在 /v1/*。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/0226zy/magents/internal/infrastructure/config"
	"github.com/0226zy/magents/internal/interfaces/httpapi"
	"github.com/0226zy/magents/internal/interfaces/webui"
	"github.com/0226zy/magents/internal/shared/deps"
)

func main() {
	configPath := flag.String("config", "conf/dsh.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if err := config.CheckDeployment(cfg); err != nil {
		log.Fatalf("部署前置检查未通过: %v", err)
	}
	ctx, err := deps.Build(cfg)
	if err != nil {
		log.Fatalf("装配失败: %v", err)
	}
	if ctx.CloseFunc != nil {
		defer ctx.CloseFunc() // 进程退出前终止 MCP server 子进程
	}
	if ctx.Runs != nil {
		if err := ctx.Runs.Recover(context.Background()); err != nil {
			// 未确认记录保持 cancelling + STOP_UNCONFIRMED，服务继续提供查询与后续恢复。
			log.Printf("Run 重启收敛未完成: %v", err)
		}
	}

	var ui http.Handler = webui.Handler()
	if cfg.Server.DisableWebUI {
		ui = http.NotFoundHandler()
	}
	srv := &http.Server{
		Addr: cfg.Server.Listen,
		Handler: httpapi.NewServer(ctx.Manager, ctx.Runs, ctx.Registry, ctx.Models, ctx.Orchestrator,
			httpapi.ServerOptions{AuthToken: cfg.Server.AuthToken, DisableModelAPI: cfg.Server.DisableModelAPI}, ui).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("正在关闭……")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	authNote := "未启用鉴权（仅适合本机）"
	if cfg.Server.AuthToken != "" {
		authNote = "已启用 Bearer 鉴权"
	}
	modelNote := ""
	if def, err := ctx.Models.Default(); err == nil {
		modelNote = fmt.Sprintf("%s（%s @ %s）", def.Name, def.Model, def.BaseURL)
	}
	models, _ := ctx.Models.List()
	fmt.Printf("dsh-go 服务端已启动\n  Web UI : http://%s/\n  API    : http://%s/v1/  (%s)\n  模型   : 缺省 %s，共 %d 个可选（资源文件 %s）\n",
		cfg.Server.Listen, cfg.Server.Listen, authNote, modelNote, len(models), cfg.ModelsFile)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}
