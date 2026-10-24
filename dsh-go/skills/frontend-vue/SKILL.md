---
name: frontend-vue
description: 当项目使用 Vue 路线时触发：写/改 Vue 3 代码时用。含 script setup 写法共识、Pinia+vue-query 状态分层、数据三态、vee-validate+zod 表单、Element Plus 按需引入、高频坑 Top10、构建规范。红线：v-html、token 存 localStorage、:key=index。关键词：Vue、Element、表单、CORS。
---

# Frontend Vue —— Vue 3 前端工程规范

> 路线声明：本项目前端栈为 Vue 3；若项目使用 React，应改用 frontend-react skill。
> 场景：写/改任何 Vue 代码。核心杠杆：Composition API 一种写法、状态按归属分层、数据区块显式三态、避开 Top 10 高频坑。

## 何时使用

1. 创建或修改任何 `.vue` 组件、composable（`use*()`）、路由页面时；
2. 需要获取/展示后端数据、写表单、管理组件状态时；
3. 排查前端报错或异常行为（响应性丢失、key 警告、CORS、env undefined、刷新 404）时；
4. 配置 Vite 构建、环境变量、Element Plus 按需引入、部署静态产物时。

## 规范做法

### 一、写法共识（R6-1）

1. **唯一写法：Composition API + `<script setup lang="ts">`**。Options API 未被废弃但新代码不默认使用；全项目只出现这一种写法，不混用。
2. **响应式默认一律 `ref()`**：适用所有类型、可整体替换（`.value =`）、避免 reactive 的解构/替换坑；`reactive()` 仅用于字段众多的表单对象，且替换时用 `Object.assign`（R6-7.2）。
3. `v-model` 组件用 `defineModel()`（Vue 3.4+）；props/emits 用类型声明式 `defineProps<T>()` / `defineEmits`（R6-1）。
4. 逻辑复用用 composable（`use*()` 函数返回 ref），**禁用 mixin**。
5. 解构必须保响应性：reactive 对象用 `toRefs`，Pinia store 的 state/getter 用 `storeToRefs`（action 可直接解构），props 传入 composable 时用 `toRef(() => props.foo)`（R6-7.1）。

### 二、状态分层：Pinia + vue-query（R6-2）

1. **分层铁律**：Pinia 只管「客户端状态」（登录用户、主题、多步表单草稿）；服务端状态（API 数据的缓存/去重/失效/重试）一律交给 `@tanstack/vue-query`，🚫 禁止手动 fetch 后塞 store（两套真相）。
2. **Pinia 使用边界**（官方原话「何时不需要 store」）：store 只放「多处访问的数据」；单组件局部状态用 ref，单页数据获取用 vue-query；🚫 什么数据都往 store 塞是头号反模式。
3. store 定义用 setup 风格（`defineStore('user', () => { ... })`），state 用 ref、getter 用 computed、action 用普通函数。
4. Vuex 已进维护模式，新项目🚫 不引入。

### 三、数据获取三态（R6-3）

1. **每个依赖远端数据的 UI 区块必须显式三态**：loading（骨架屏优先）、error（给重试按钮）、success；empty 态在 success 分支内判 `data.length === 0`，给引导性文案而非白屏。
2. 标准写法：`useQuery` 返回 `isPending / isError / data`，模板用 `v-if / v-else-if / v-else` 链；官方顺序——先查 isPending，再查 isError，然后即可假设成功。
3. `queryKey` 直接放 ref（自动解包），key 变化自动重取，🚫 不手写 watch 做数据获取。
4. mutation（增删改）用 `useMutation`，成功后 `queryClient.invalidateQueries` 让列表自动刷新，不手动同步两份 state。
5. 所有请求走统一 `apiClient`（baseURL、`if (!res.ok) throw`、401 统一跳登录）；🚫 组件内裸 fetch 或手写 ref+try/catch 数据层（缺竞态/缓存/重试）。

### 四、表单与校验（R6-4）

1. **≤3-4 个字段、无复杂校验**（登录/搜索类）：原生 `v-model` 受控 + 提交时简单判断即可，不值得引库。
2. **超过此复杂度**（逐字段错误提示、嵌套对象、数组字段）：一律 vee-validate + zod，经 `@vee-validate/zod` 的 `toTypedSchema` 桥接；zod schema 先行定义（即数据契约），`useForm({ validationSchema })` 接管校验，`handleSubmit` 保证提交数据类型安全。
3. 跨字段校验用 zod `.refine()`；异步校验（如用户名查重）用 async validator 并加防抖。
4. 已选 Element Plus 时，`el-form` 自带 async-validator 规则体系可作中间档（与组件库耦合但零额外依赖）——一个项目只选一档，不混用。
5. 提交按钮在 mutation pending 期间必须 disabled 并显示加载文案，防重复提交。

### 五、UI 组件库（R6-6）

1. **默认 Element Plus**：生态与 LLM 语料最大（生成代码幻觉最少）、组件全（表格/表单/日期/树/上传）、中英文档完善、TS 支持良好。
2. **按需引入控制体积**：用 `unplugin-vue-components` 自动按需导入（全量引入 1.5-2.5MB，按需后 500-800KB）；🚫 不 `app.use(ElementPlus)` 全量注册。
3. **备选 Naive UI**：TS 原生、类型体验最佳、性能更好；用户明确要「现代简洁风/极致类型」时切换。默认不用 Vuetify / Ant Design Vue。
4. 样式体系只用「组件库 + scoped 样式」，🚫 默认不引入 Tailwind/UnoCSS（与组件库样式体系易冲突，R6-9）。

### 六、目录结构（R6-5）

create-vue 基线（单领域小应用直接用，`npm create vue@latest` 勾选 TS/Router/Pinia）：

```
src/
├── assets/       # 会被构建处理的静态资源（哈希指纹）
├── components/   # 可复用展示型组件
├── views/        # 路由级页面组件
├── router/       # vue-router 配置
├── stores/       # Pinia stores
├── composables/  # use*() 逻辑复用
├── lib/          # apiClient 等基础设施
├── App.vue / main.ts
```

多领域应用演进为 feature-based：`src/features/<domain>/` 各自内含 components/composables/store/api/routes，跨域共享代码放 `src/shared/`。判断标准：「改一个功能要跨四五个不相关目录时就该按 feature 分组」。`@` 别名指向 `src/`（create-vue 默认）。

### 七、构建与打包规范（R6-8）

1. **路由级懒加载是默认动作**：除首页外所有路由写 `component: () => import('../views/XxxView.vue')`，Vite 自动分包；大依赖（图表、编辑器）用 `defineAsyncComponent` 二次拆分。路由表加 catch-all 404 页。
2. **静态资源**：会被处理的小资源放 `src/assets` 走 import；原样拷贝的大文件放 `public/`，引用时用 `import.meta.env.BASE_URL + 'file'` 拼路径。
3. **env 规范**：`.env`（公共）/`.env.development`/`.env.production`/`.env.local`（git ignore）；代码统一 `import.meta.env.VITE_X` **字面量点访问**（动态 `import.meta.env[key]` 构建期不生效）；只有 `VITE_` 前缀变量暴露给客户端，**密钥绝不加 VITE_ 前缀**；改 .env 后重启 dev server；在 `vite-env.d.ts` 声明类型。
4. **缓存策略**：构建产物带哈希的 `/assets/` 设 immutable 长缓存，`index.html` 禁缓存。
5. **SPA fallback**：history 模式部署必须配「未命中路径返回 index.html」——nginx `try_files $uri $uri/ /index.html;`、Netlify 在 `public/_redirects` 写 `/* /index.html 200`、Vercel 配 rewrite；或直接用 `createWebHashHistory()` 规避（URL 丑但零配置）（R6-7.7）。
6. **开发代理**：vite.config `server.proxy: { '/api': { target, changeOrigin: true } }`，浏览器视角同源，开发期无 CORS；代码里写绝对 URL 会导致请求绕过代理（R6-7.8）。
7. 构建校验：`vue-tsc` 类型检查 + `vite build`（create-vue 的 `build` 脚本默认 `run-p type-check "build-only"`）。
8. **base 与别名配套**：部署子路径时 `vite.config` 的 `base: '/sub/'` 与 `createWebHistory(import.meta.env.BASE_URL)` 需配套；`@` 别名在 vite `resolve.alias` 与 tsconfig `paths` 同步配置（R6-7.10）。

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

### 2. 带三态的列表组件（web/src/features/orders/OrderList.vue）

```vue
<script setup lang="ts">
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { apiClient } from "../../lib/apiClient";

interface Order {
  id: string;
  title: string;
  created_at: string;
}

const queryClient = useQueryClient();
const { data, isPending, isError, error, refetch } = useQuery({
  queryKey: ["orders"],
  queryFn: () => apiClient<{ data: Order[]; total: number }>("/items?limit=100&offset=0"),
});

const deleteMutation = useMutation({
  mutationFn: (id: string) => apiClient<void>(`/items/${id}`, { method: "DELETE" }),
  onSuccess: () => queryClient.invalidateQueries({ queryKey: ["orders"] }),
});
// 从返回值中解构出 ref，模板里才可自动解包
const { mutate: deleteOrder, isPending: isDeleting } = deleteMutation;
</script>

<template>
  <div v-if="isPending" aria-busy="true">
    <p>加载中…</p>
  </div>
  <div v-else-if="isError" role="alert">
    <p>加载失败：{{ error?.message }}</p>
    <button @click="refetch()">重试</button>
  </div>
  <div v-else-if="!data || data.data.length === 0">
    <p>还没有订单</p>
    <p>点击右上角「新建」创建第一个吧。</p>
  </div>
  <ul v-else>
    <li v-for="o in data.data" :key="o.id">
      {{ o.title }}
      <button :disabled="isDeleting" @click="deleteOrder(o.id)">删除</button>
    </li>
  </ul>
</template>
```

### 3. vee-validate + zod 表单（web/src/features/orders/OrderForm.vue）

```vue
<script setup lang="ts">
import { ref } from "vue";
import { useForm } from "vee-validate";
import { toTypedSchema } from "@vee-validate/zod";
import { z } from "zod";
import { useMutation, useQueryClient } from "@tanstack/vue-query";
import { apiClient } from "../../lib/apiClient";

const orderSchema = z.object({
  title: z.string().min(1, "标题不能为空").max(100, "标题最长 100 字"),
  description: z.string().max(500, "描述最长 500 字").optional(),
});
type OrderFormValues = z.infer<typeof orderSchema>;

const emit = defineEmits<{ created: [] }>();
const queryClient = useQueryClient();
const submitError = ref("");

const { handleSubmit, defineField, errors } = useForm<OrderFormValues>({
  validationSchema: toTypedSchema(orderSchema),
});
const [title, titleAttrs] = defineField("title");
const [description, descriptionAttrs] = defineField("description");

const createMutation = useMutation({
  mutationFn: (values: OrderFormValues) =>
    apiClient("/items", { method: "POST", body: JSON.stringify(values) }),
  onSuccess: () => {
    queryClient.invalidateQueries({ queryKey: ["orders"] });
    emit("created");
  },
  onError: (err) => {
    submitError.value = err instanceof Error ? err.message : "提交失败，请重试";
  },
});
const { mutate: createOrder, isPending: isSubmitting } = createMutation;

const onSubmit = handleSubmit((values) => createOrder(values));
</script>

<template>
  <form @submit="onSubmit">
    <div>
      <label for="title">标题</label>
      <input id="title" v-model="title" v-bind="titleAttrs" />
      <p v-if="errors.title" role="alert">{{ errors.title }}</p>
    </div>
    <div>
      <label for="description">描述</label>
      <textarea id="description" v-model="description" v-bind="descriptionAttrs" />
      <p v-if="errors.description" role="alert">{{ errors.description }}</p>
    </div>
    <p v-if="submitError" role="alert">{{ submitError }}</p>
    <button type="submit" :disabled="isSubmitting">
      {{ isSubmitting ? "提交中…" : "创建" }}
    </button>
  </form>
</template>
```

## 反模式与红线

### 高频坑 Top 10（症状 → 根因 → 解法）

**Vue 特有（详述）：**

| # | 症状 | 根因 | 解法 |
|---|------|------|------|
| 1 | 父组件改了值子组件不更新；store 变了页面不变 | 解构把 proxy 属性快照成普通值，脱离依赖追踪（`const { x } = props/store/reactive`） | `toRefs(state)` / `storeToRefs(store)` / `toRef(() => props.foo)`；store 的 action 可直接解构，state/getter 必须 storeToRefs（R6-7.1） |
| 2 | `reactive` 对象整体重新赋值后视图不更新 | reactive 不能整体替换、不能解构、仅限对象类型 | 默认一律 `ref()`，整体替换走 `.value =`；reactive 仅用于多字段表单且用 `Object.assign` 替换（R6-7.2） |
| 3 | 控制台 prop 突变警告 + 父子数据不同步 | props 只读，单向数据流，子组件直接改 prop | 双向绑定用 `defineModel()`（3.4+），或 emit 事件让父组件改（R6-7.3） |
| 4 | 列表重排/删除后输入框内容错位、动画异常 | `v-for` 的 `:key` 用 index 或不写 | `:key` 用稳定唯一业务 id，🚫 禁止 `:key="index"`（静态列表除外）（R6-7.4） |
| 5 | watch 回调地狱；慢的旧请求覆盖新数据 | 能用 computed 的派生状态用了 watch；数据获取手写 watch | 派生值用 computed；副作用才用 watch 并返回 cleanup 处理竞态；数据获取交给 vue-query 的 reactive queryKey；避免对大对象无脑 `deep: true`（R6-7.5） |
| 6 | 重复请求、不可预测的更新 | computed 里写了副作用（请求/改状态） | computed 必须是纯函数，副作用移到 watch/事件（R6-7.6） |

**与 React 共性（简述）：**

| # | 症状 | 根因 | 解法 |
|---|------|------|------|
| 7 | 部署后刷新 `/dashboard` 404 | history 模式下服务器上没有该文件 | nginx `try_files` / `_redirects` / vercel rewrite；或 `createWebHashHistory()` 规避（R6-7.7） |
| 8 | 开发时请求被 CORS 拦 | `localhost:5173 → :3000` 跨源；或代码写了绝对 URL 绕过代理 | vite `server.proxy` 代理 `/api` + `changeOrigin`；生产 nginx 同源反代（R6-7.8） |
| 9 | `import.meta.env.XXX` undefined | 缺 `VITE_` 前缀；改 .env 后没重启 dev server | 前缀 + 重启 + `vite-env.d.ts` 声明类型；密钥绝不加 `VITE_` 前缀（R6-7.9） |
| 10 | 部署子路径白屏；`@` 导入报错 | `base` 与 router history 不配套；vite alias 与 tsconfig paths 不同步 | `base` + `createWebHistory(BASE_URL)` 配套；两处别名同步配置（R6-7.10） |

### 红线（脚本可查）

- 🚫 `v-html` 渲染用户内容（XSS；确需富文本必须先用 DOMPurify 净化）（R3-四）。
- 🚫 token / 会话凭证写入 localStorage（XSS 一键窃取；本栈用 HttpOnly+Secure+SameSite cookie session）（R3-红线5）。
- 🚫 `:key="index"` / `:key="i"`（R6-7.4）。
- 🚫 浏览器代码中使用 `process.env.X` 或动态 `import.meta.env[key]`（R6-7.9）。
- 🚫 手动 fetch 数据塞 Pinia store、绕过 vue-query（R6-2）。
- 🚫 数据区块缺 loading/error/empty 任一分支（R6-3）。
- 🚫 Element Plus 全量 `app.use(ElementPlus)` 注册（R6-6）。
- 🚫 `app.config.errorHandler` 之外各自为政的请求错误处理；组件内裸 fetch 绕过 apiClient。

## 交付前检查清单

- [ ] 全部组件为 `<script setup lang="ts">`，无 Options API / mixin 混用
- [ ] 响应式默认 ref()；解构处均有 toRefs / storeToRefs / toRef
- [ ] 服务端状态全走 vue-query，Pinia 只存跨组件客户端状态
- [ ] 每个远端数据区块都有 loading/error/empty/success 分支
- [ ] 所有请求走统一 apiClient，组件内无裸 fetch、无手写 watch 取数
- [ ] 表单按复杂度分档：≤3-4 字段 v-model；复杂表单 vee-validate + zod；提交按钮 pending 期 disabled
- [ ] 列表 `:key` 用稳定唯一 id
- [ ] Element Plus 按需引入（unplugin-vue-components），无全量注册
- [ ] 路由全懒加载 + catch-all 404；env 全部 `VITE_` 前缀字面量访问；部署已配 SPA fallback
- [ ] 运行 `python3 ../frontend-react/scripts/check_frontend.py <web目录>` 无红线命中

## 长尾不做（本 skill 域内）

- **Nuxt / SSR**：工具型应用几乎不需要 SEO 与服务端渲染，SSR 引入 hydration、组件库适配等一整类新坑（R6-9）；
- **微前端**：单人单应用无意义（R6-9）；
- **i18n**：除非用户明确要多语言，否则硬编码中文（R6-9）；
- **复杂动画**：用组件库内置过渡即可，不引 GSAP/自写物理动画（R6-9）；
- **Tailwind/UnoCSS**：与组件库样式体系易冲突，默认只用组件库 + scoped 样式（用户点名要再加）（R6-9）；
- 单元测试/E2E：一次性交付场景权重低，可选配 Vitest，默认不生成（R6-9）；
- Vuex、Vuetify、Ant Design Vue：默认不引入（R6-2、R6-6）。
