// Package skill 是工程技能资源的本地文件加载器（agentskills.io 兼容格式）。
//
// 目录约定：
//
//	skills/
//	├── arch-design/
//	│   ├── SKILL.md        # YAML frontmatter（name/description）+ 正文
//	│   └── scripts/…       # 可选：验证脚本等附属资产
//	└── …
//
// 启动期一次性扫描；skill 是只读资产（随仓库分发），运行期不修改，
// 因此不需要仓储写接口——与 LLMInfoRepo 不同，这里刻意不做 CRUD。
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/0226zy/magents/internal/domain/entity"
)

// frontmatter 是 SKILL.md 的头部元数据。
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// LoadDir 扫描 dir 下的全部 skill 子目录（含 SKILL.md 者），按 name 排序返回。
// dir 不存在或无任何 skill 时返回空切片（调用方据此决定不启用技能能力——
// 配置决定启用，对齐插件哲学）；单个 SKILL.md 损坏则整体报错，
// 配置错误应暴露在启动期而非第一次会话时。
func LoadDir(dir string) ([]entity.Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("扫描技能目录 %s: %w", dir, err)
	}

	var skills []entity.Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "SKILL.md")
		raw, err := os.ReadFile(path)
		if err != nil {
			continue // 无 SKILL.md 的目录不是 skill（如文档目录），跳过
		}
		sk, err := parse(e.Name(), string(raw))
		if err != nil {
			return nil, fmt.Errorf("解析 %s: %w", path, err)
		}
		abs, _ := filepath.Abs(filepath.Join(dir, e.Name()))
		sk.Dir = abs
		skills = append(skills, sk)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

// parse 拆分 frontmatter 与正文并校验。dirName 用于交叉校验 name 一致性。
func parse(dirName, content string) (entity.Skill, error) {
	var sk entity.Skill
	if !strings.HasPrefix(content, "---\n") {
		return sk, fmt.Errorf("缺少 YAML frontmatter（应以 --- 开头）")
	}
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return sk, fmt.Errorf("frontmatter 未闭合（缺少结尾 ---）")
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		return sk, fmt.Errorf("frontmatter YAML 非法: %w", err)
	}
	if fm.Name == "" {
		return sk, fmt.Errorf("frontmatter 缺少 name")
	}
	if fm.Name != dirName {
		return sk, fmt.Errorf("frontmatter name %q 与目录名 %q 不一致", fm.Name, dirName)
	}
	if fm.Description == "" {
		return sk, fmt.Errorf("frontmatter 缺少 description（模型据此判断触发时机）")
	}
	body := strings.TrimSpace(rest[end+len("\n---\n"):])
	if body == "" {
		return sk, fmt.Errorf("SKILL.md 正文为空")
	}
	return entity.Skill{Name: fm.Name, Description: fm.Description, Body: body}, nil
}

// Catalog 生成注入系统提示词的技能目录清单（静态前缀组成部分，逐字节稳定）。
// 模型据此判断何时用 use_skill 加载哪份技能正文。
func Catalog(skills []entity.Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n# 工程技能库\n\n")
	b.WriteString("以下技能沉淀了成熟的软件工程经验（架构取舍、代码模式、验证门禁）。\n")
	b.WriteString("任务命中某技能的触发时机时，先用 use_skill 工具加载其正文再动手——\n")
	b.WriteString("不要凭通识动手，先取经验。技能附属的验证脚本可直接用 bash 运行。\n\n")
	for _, sk := range skills {
		fmt.Fprintf(&b, "- **%s**：%s\n", sk.Name, sk.Description)
	}
	return b.String()
}
