// Package webui 内嵌 Web 控制台（对齐 dsh web 形态）：
// 会话列表 / 聊天视图 / 审批裁决 / fork / 模式切换，事件经 SSE 实时推送。
//
// 静态资源按文件拆分（static/index.html + app.css + app.js，评审 P1），
// 经 embed.FS 内嵌进二进制——交付形态不变（单文件可执行），
// 但样式/逻辑/结构可独立维护，后续加审批/模型管理界面时按文件扩展即可。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// Handler 返回 Web UI 的 http.Handler："/" 与静态资源（/app.css、/app.js）。
// 未命中的路径一律 404（本 handler 是 API 路由之后的兜底，不做 SPA 内部路由）。
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("webui: 内嵌静态资源损坏: " + err.Error())
	}
	return http.FileServerFS(sub)
}
