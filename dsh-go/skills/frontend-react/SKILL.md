---
name: frontend-react
description: 当项目使用 React 路线时触发：写/改 React 代码时用。含状态三分层、TanStack Query 四态、RHF+zod 表单、高频坑 Top10、路由级 code splitting、SPA fallback、构建规范。红线：dangerouslySetInnerHTML、token 存 localStorage、key={index}。关键词：写组件、表单、CORS、刷新 404。
---

# Frontend React —— React 前端工程规范

> 路线声明：本项目前端栈为 React；若项目使用 Vue，应改用 frontend-vue skill。
> 场景：写/改任何 React 代码。核心杠杆：状态按归属分层、数据区块显式四态、表单走 RHF+zod、避开 Top 10 高频坑。

## 何时使用

1. 创建或修改任何 `.tsx/.jsx` 组件、hook、页面时；
2. 需要获取/展示后端数据、写表单、管理组件状态时；
3. 排查前端报错或异常行为（无限重渲染、key 警告、CORS、env undefined）时；
4. 配置 Vite 构建、环境变量、部署静态产物时。

## 规范做法

### 一、状态三分层决策树（R1-一）

写任何状态前先问「它归谁」，按决策树选工具：

```
这个值能从 URL / 服务端 / 其他 state 派生吗？
├─ 能 → 不存，render 时直接计算（贵才用 useMemo）
└─ 不能 → 它是什么归属？
   ├─ 来自后端的数据（异步、可过期、多人共享）
   │   → TanStack Query，🚫禁止 useEffect+useState 手动缓存进全局 store（两套真相）
   ├─ 刷新后希望保留的视图参数（页码、排序、关键词、tab）
   │   → URL search params（useSearchParams），刷新/分享可复现、后退可用
   ├─ 只在一个组件用 → useState
   ├─ 同页多个组件共享 → 提升 state 或 Context
   └─ 跨页面的 UI 偏好（主题、侧边栏）→ 轻量 store（Zustand）或 localStorage
```

默认不引入 Redux 级全局 store；useState + Context 覆盖绝大多数 SPA 场景（R1-一.3）。

### 二、数据获取四态标准写法（R1-二）

1. **每个依赖远端数据的 UI 区块都必须显式处理四态**：loading（骨架屏优先于 spinner）、error（给重试按钮，不是一行红字）、empty（数据成功但为空时给引导性文案/空态，不是白屏）、success。这是组件级硬要求（R1-5）。
2. 标准写法：`const { data, isPending, isError, error, refetch } = useQuery(...)`；先 early return loading/error 分支，空态判断 `data.length === 0` 放在 success 分支内（R1-6）。
3. mutation（增删改）用 `useMutation`，成功后 `queryClient.invalidateQueries` 让相关列表自动刷新，不手动同步两份 state（R1-6）。
4. 🚫 不用裸 `useEffect + fetch` 手写数据层：缺竞态处理、无缓存、无去重、无重试（R1-7）。
5. 所有请求走统一 `apiClient`（baseURL、错误规范化、401 统一跳登录）；fetch 对 4xx/5xx 不抛错，封装层必须 `if (!res.ok) throw`（R1-坑8）。

### 三、表单范式（R1-三）

1. 简单表单（1-3 字段，如登录）受控组件 + useState 足够；多字段/有校验/有动态字段一律 React Hook Form（R1-9）。
2. 用 zod 定义 schema + `zodResolver` 接入，类型 `z.infer` 推导——一处定义得到运行时校验 + TS 类型 + 错误信息（R1-10）。
3. 校验时机：默认 onSubmit；长表单 `mode: 'onBlur'` + `reValidateMode: 'onChange'`；🚫 避免默认 onChange 全量校验（R1-11）。
4. 提交期服务端错误（如 409 邮箱已占用）用 `setError('email', { message })` 回写字段，或 `setError('root', ...)` 表单级错误（R1-10）。
5. 提交按钮在 mutation pending 期间必须 disabled 并显示加载文案，防重复提交（R1-12）。

### 四、构建与打包规范（R1-六）

1. **路由级 code splitting**：`const Dashboard = lazy(() => import('./pages/Dashboard'))` + `<Suspense fallback={<PageSkeleton/>}>`；组件级拆分只用于「重且首屏不可见」的模块（图表/富文本/地图）；🚫 不给小组件做 lazy（瀑布请求更慢）（R1-16）。
2. **环境变量**：统一 `VITE_` 前缀 + `import.meta.env.VITE_X` 字面量点访问（动态 `import.meta.env[key]` 不生效）；提供 `.env.example`；改 env 后重启 dev server / 重新 build；🚫 浏览器代码里写 `process.env.X`；`VITE_` 变量进公开 bundle，绝不放密钥（R1-坑5）。
3. **SPA fallback**：部署平台必须配置「未命中静态资源的路径返回 index.html」——Netlify 在 `public/_redirects` 写 `/* /index.html 200`，Vercel 配 `vercel.json` rewrite，Nginx 用 `try_files $uri /index.html`；应用内加 `path="*"` 兜底 404 页；上线前直接访问深层 URL 验证（R1-坑6）。
4. **开发代理**：Vite `server.proxy` 把 `/api` 代理到后端并 `changeOrigin: true`，浏览器视角同源，开发期无 CORS（R1-坑4）。
5. **静态资源**：小图/图标放 `src/assets`（带 hash URL 可长效缓存）；大文件放 `public/` 用 `/xxx.png` 引用；图片加 `loading="lazy"` 与宽高属性防布局抖动（R1-18）。
6. 构建优化基线：`vite build` 默认 minify/tree-shaking；需要时用 `manualChunks` 拆大依赖；上线前用 `vite preview` 或部署预览验证产物（R1-19）。

## 代码骨架

### 1. 统一 apiClient（web/src/lib/apiClient.ts）

```ts
// 全应用唯一请求出口：baseURL + 错误规范化 + 401 统一跳转
export async function apiClient<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  if (res.status === 401) {
    window.location.href = "/login";
    throw new Error("unauthorized");
  }
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new Error(body?.error?.message ?? `请求失败（${res.status}）`);
  }
  return res.status === 204 ? (undefined as T) : res.json();
}
```

### 2. 四态列表组件（features/orders/components/OrderList.tsx）

```tsx
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../../../lib/apiClient";

interface Order {
  id: string;
  title: string;
  created_at: string;
}

export function OrderList() {
  const queryClient = useQueryClient();
  const { data, isPending, isError, error, refetch } = useQuery({
    queryKey: ["orders"],
    queryFn: () => apiClient<{ data: Order[]; total: number }>("/items?limit=100&offset=0"),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => apiClient<void>(`/items/${id}`, { method: "DELETE" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["orders"] }),
  });

  if (isPending) return <div className="animate-pulse space-y-2">{/* 骨架屏 */}<div className="h-12 rounded bg-gray-200" /><div className="h-12 rounded bg-gray-200" /></div>;
  if (isError) return <div role="alert"><p>加载失败：{error.message}</p><button onClick={() => refetch()}>重试</button></div>;

  const orders = data.data;
  if (orders.length === 0) {
    return <div className="text-center text-gray-500"><p>还没有订单</p><p>点击右上角「新建」创建第一个吧。</p></div>;
  }
  return (
    <ul>
      {orders.map((o) => (
        <li key={o.id}>
          {o.title}
          <button disabled={deleteMutation.isPending} onClick={() => deleteMutation.mutate(o.id)}>删除</button>
        </li>
      ))}
    </ul>
  );
}
```

### 3. RHF + zod 表单（features/orders/components/OrderForm.tsx）

```tsx
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "../../../lib/apiClient";

const orderSchema = z.object({
  title: z.string().min(1, "标题不能为空").max(100, "标题最长 100 字"),
  description: z.string().max(500, "描述最长 500 字").optional(),
});
type OrderFormValues = z.infer<typeof orderSchema>;

export function OrderForm({ onCreated }: { onCreated: () => void }) {
  const queryClient = useQueryClient();
  const {
    register,
    handleSubmit,
    setError,
    formState: { errors },
  } = useForm<OrderFormValues>({
    resolver: zodResolver(orderSchema),
    mode: "onBlur",
    reValidateMode: "onChange",
  });

  const createMutation = useMutation({
    mutationFn: (values: OrderFormValues) =>
      apiClient("/items", { method: "POST", body: JSON.stringify(values) }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["orders"] });
      onCreated();
    },
    onError: (err) => setError("root", { message: err.message }),
  });

  return (
    <form onSubmit={handleSubmit((v) => createMutation.mutate(v))}>
      <div>
        <label htmlFor="title">标题</label>
        <input id="title" {...register("title")} />
        {errors.title && <p role="alert">{errors.title.message}</p>}
      </div>
      <div>
        <label htmlFor="description">描述</label>
        <textarea id="description" {...register("description")} />
        {errors.description && <p role="alert">{errors.description.message}</p>}
      </div>
      {errors.root && <p role="alert">{errors.root.message}</p>}
      <button type="submit" disabled={createMutation.isPending}>
        {createMutation.isPending ? "提交中…" : "创建"}
      </button>
    </form>
  );
}
```

## 高频坑 Top 10（症状 → 根因 → 解法）

| # | 症状 | 根因 | 解法 |
|---|------|------|------|
| 1 | effect 套 effect、派生 state 不同步、无限循环 | 把 effect 当「响应 state 变化」的通用钩子；它只用于「与外部系统同步」 | 派生数据渲染期直接算；响应用户操作放事件处理器；prop 变化重置 state 用 `key={id}`（R1-坑1） |
| 2 | 删除/重排后输入框内容「串行」到别的行；控制台 unique key 警告 | `key={index}`：删除首项后所有 index 偏移，state 挂错行 | 用数据稳定唯一 id（数据库 id / crypto.randomUUID()）；🚫 `key={Math.random()}`（R1-坑2） |
| 3 | setInterval 回调里 count 永远是初始值；监听里读到旧 props | 依赖数组 `[]` 使闭包停留在首次渲染快照 | 函数式 `setCount(c => c + 1)`；读最新值用 ref；开启 eslint-plugin-react-hooks，不手动省依赖（R1-坑3） |
| 4 | 开发时 fetch 报 "blocked by CORS policy" | CORS 是服务端响应头问题；5173↔8000 即跨源 | dev 用 Vite `server.proxy` 代理 `/api`（同源）；生产同源部署或后端枚举白名单，🚫 不开 `*`（R1-坑4） |
| 5 | `import.meta.env.VITE_API_URL` 是 undefined，请求打到 `undefined/users` | 缺 `VITE_` 前缀 / 写了 `process.env` / 改完 .env 没重启 / 部署平台构建后才设变量 | `VITE_` 前缀 + 字面量点访问；提供 .env.example；改后重启/重构建；密钥绝不入前端（R1-坑5） |
| 6 | 部署后刷新 `/dashboard/settings` 或分享链接 404 | SPA 路由由前端 JS 完成，服务器上没有该文件 | 平台配 SPA fallback（`_redirects` / vercel.json rewrite / nginx `try_files`）；加 `path="*"` 404 页（R1-坑6） |
| 7 | token 放 localStorage，XSS 一键偷走 | localStorage 对页面内任何 JS 可读 | 本栈用 HttpOnly+Secure+SameSite cookie session；🚫 不在 localStorage 存有权限的 token（R1-坑7、R3-一） |
| 8 | 后端返回 500 却走进 success 分支，`data.xxx` undefined | fetch 只在网络层失败 reject，4xx/5xx 照样 resolve | apiClient 封装 `if (!res.ok) throw`；配合 isError 展示错误态（R1-坑8） |
| 9 | 页面无限重渲染，"Maximum update depth exceeded" | 渲染函数体内直接调 setState；effect 更新自己依赖的 state | setState 只出现在事件处理器或 effect；effect 更新 state 时确认依赖收敛（R1-坑9） |
| 10 | 输入框每敲一字整页闪；Context 变化全场重渲染 | 高频 state 放顶层；大 Context 的 value 每次渲染新建引用 | 状态下沉到最近公共祖先；Context 按关注点拆分；先测量再优化，🚫 预防性 memo 满天飞（R1-坑10） |

## 反模式与红线

- 🚫 `dangerouslySetInnerHTML` 渲染用户内容（XSS；确需富文本必须 DOMPurify 净化）（R3-四）。
- 🚫 token / 会话凭证写入 localStorage（R3-红线5）。
- 🚫 `key={index}` / `key={i}` / `key={Math.random()}`（R1-坑2）。
- 🚫 浏览器代码中使用 `process.env.X` 或动态 `import.meta.env[key]`（R1-坑5）。
- 🚫 裸 `useEffect + fetch` 手写数据层（R1-7）。
- 🚫 数据区块缺 loading/error/empty 任一分支（R1-5）。
- 🚫 10+ 字段表单用 useState 逐个管理（R1-9）。
- 🚫 给生产后端开 `Access-Control-Allow-Origin: *` 来「解决」CORS（R1-坑4）。

## 交付前检查清单

- [ ] 每个远端数据区块都有 loading（骨架）/error（重试）/empty（引导）/success 四态
- [ ] 所有请求走统一 apiClient，组件内无裸 fetch、无 useEffect+fetch
- [ ] 表单走 RHF+zod（或 ≤3 字段受控），提交按钮 pending 期 disabled
- [ ] 列表 key 用稳定唯一 id；筛选/分页等视图参数在 URL 中
- [ ] 路由级 lazy + Suspense；`path="*"` 404 页存在
- [ ] `.env.example` 齐全；env 全部 `VITE_` 前缀字面量访问
- [ ] 部署平台已配 SPA fallback，深层 URL 直接访问验证通过
- [ ] 运行 `python3 scripts/check_frontend.py <web目录>` 无红线命中

## 长尾不做（本 skill 域内）

SSR/SSG（Next.js）、微前端、Redux/重型状态机、乐观更新、PWA/离线同步、i18n 框架、WebSocket 实时协作、复杂动画编排、Storybook、E2E 全家桶、预防性性能优化（memo 满天飞/虚拟列表）——默认不做，除非用户明确要求（R1-附）。
