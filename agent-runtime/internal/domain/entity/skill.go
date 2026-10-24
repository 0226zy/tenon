package entity

// Skill 是一份「工程技能」资源：面向特定任务域的程序性知识包
// （agentskills.io 兼容格式：一个目录，内含 SKILL.md 与可选 scripts/）。
//
// 设计哲学（对齐 skills-pack 的分层）：能下沉为确定性知识的，不留给模型推理——
// skill 承担「怎么做」的工程经验（架构取舍、代码模式、验证门禁），
// 模型只在加载后把经验应用到用户的具体业务上。
//
// 加载方式：启动期由组合根从 skills_dir 扫描（见 infrastructure/adapter/skill），
// 目录清单（name+description）注入系统提示词静态前缀，正文经 use_skill 工具
// 按需加载（progressive disclosure：不全部进上下文，按需取用）。
type Skill struct {
	// Name 是唯一标识（= 目录名 = frontmatter name）。
	Name string
	// Description 是触发时机说明（frontmatter description），
	// 进入系统提示词的目录清单，模型据此判断何时加载。
	Description string
	// Body 是 SKILL.md 正文（frontmatter 之后的全部内容）。
	Body string
	// Dir 是 skill 目录的绝对路径（scripts/ 等附属资产的位置，
	// 正文里引用脚本时模型用 bash 执行）。
	Dir string
}
