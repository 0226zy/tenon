# Tenon Web

Tenon 的 P0 Web 前端，使用 Vue 3、Vite、TypeScript、Pinia 和只读 Monaco Editor。

## 环境要求

- Node.js 20 或更高版本
- npm 10 或更高版本
- 真实 control / wsd API 服务，默认地址为 `http://127.0.0.1:8080`
- 可选的本地预览服务，默认地址为 `http://127.0.0.1:8081`

本项目不包含 mock API。登录、对话、事件流、文件和预览等功能均需要真实后端；仅启动前端无法完成端到端体验。

## 本地开发

在本目录执行：

```bash
npm install
npm run dev
```

Vite 默认监听 `http://127.0.0.1:7100`。可通过 CLI 参数覆盖监听地址和端口，例如：

```bash
npm run dev -- --host 0.0.0.0 --port 7100
```

开发服务器代理规则：

| 浏览器路径 | 默认目标 | 用途 |
| --- | --- | --- |
| `/api` | `http://127.0.0.1:8080` | control / wsd API 与 SSE |
| `/preview-local` | `http://127.0.0.1:8081` | 可选的本地预览服务，代理时移除路径前缀 |

如服务地址不同，请修改 `vite.config.ts` 中对应的 `target`。`/api` 代理使用 `changeOrigin: true`，并仅为可信本地开发将 Origin 固定为后端配置的来源；`/preview-local` 保持 `changeOrigin: false`。生产环境仍执行完整 CSRF 校验。

## 测试与构建

```bash
npm run test -- --run
npm run build
```

`npm run build` 会先执行 Vue / TypeScript 类型检查，再生成生产资源到 `dist/`。

生产部署时应将 `dist/` 作为静态站点发布，并为前端路由配置 SPA 回退：未匹配到静态资源的前端路径回退到 `index.html`。同时保留 `/api` 到 control / wsd 服务的代理；需要本地预览时，还要按开发配置保留 `/preview-local` 代理。不要将 `/api` 或 `/preview-local` 请求回退到 `index.html`。

## 主要目录

- `src/api`：API、超时与统一错误处理
- `src/events`：SSE 连接管理与事件 reducer
- `src/stores`：认证、对话、聊天、文件和预览状态
- `src/features`：登录、对话、聊天、代码、预览与整体布局
- `src/types/contract.ts`：与服务端契约对齐的 DTO
