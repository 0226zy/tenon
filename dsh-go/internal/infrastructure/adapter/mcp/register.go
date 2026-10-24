package mcp

import (
	"context"
	"fmt"

	"github.com/0226zy/magents/internal/domain/plugin"
)

// RegisterServers 按配置启动全部 MCP server，把它们的工具注册进注册表。
// 返回的 closeFn 终止全部 server 子进程（进程退出清理）；
// readOnly 是声明 read_only 的 server 所提供的工具名列表（供审批钩子放行）。
// 任一 server 接入失败即回滚已启动的 server 并返回错误——
// 配置错误暴露在启动期，而不是第一次工具调用时。
func RegisterServers(registry *plugin.Registry, cfgs []ServerConfig, maxOutput int) (closeFn func(), readOnly []string, err error) {
	var clients []*Client
	// 局部变量捕获：return 语句会把命名返回值 closeFn 置 nil，
	// defer 必须引用局部副本，回滚才不会空指针。
	closers := func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}
	closeFn = closers
	defer func() {
		if err != nil {
			closers()
		}
	}()

	for _, cfg := range cfgs {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.timeoutOf())
		client, err := Start(ctx, cfg)
		cancel()
		if err != nil {
			return nil, nil, err
		}
		ctx, cancel = context.WithTimeout(context.Background(), cfg.timeoutOf())
		remoteTools, err := client.ListTools(ctx)
		cancel()
		if err != nil {
			_ = client.Close()
			return nil, nil, fmt.Errorf("MCP server %q 拉取工具列表失败: %w", cfg.Name, err)
		}
		clients = append(clients, client)

		for _, rt := range remoteTools {
			adapter := &toolAdapter{
				client:    client,
				server:    cfg.Name,
				remote:    rt,
				timeout:   cfg.timeoutOf(),
				maxOutput: maxOutput,
			}
			if err := registry.RegisterTool(adapter); err != nil {
				return nil, nil, fmt.Errorf("注册 MCP 工具失败: %w", err)
			}
			if cfg.ReadOnly {
				readOnly = append(readOnly, adapter.fullName())
			}
		}
	}
	return closeFn, readOnly, nil
}
