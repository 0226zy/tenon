package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/0226zy/magents/internal/domain/entity"
	"github.com/0226zy/magents/internal/domain/port"
)

// UseSkillTool 是工程技能的按需加载器（progressive disclosure 的取件口）：
// 目录清单已在系统提示词里，模型判断触发后调用本工具取回 skill 正文。
// 只读、免审批、并发安全。
type UseSkillTool struct {
	skills map[string]entity.Skill
	names  []string // 排序后的技能名（错误提示与 list 用，稳定）
	maxOut int      // 输出截断阈值（与其他工具一致，超长走 spill）
}

// NewUseSkillTool 以启动期加载的技能快照构造工具。
func NewUseSkillTool(skills []entity.Skill, maxOut int) *UseSkillTool {
	t := &UseSkillTool{skills: map[string]entity.Skill{}, maxOut: maxOut}
	for _, sk := range skills {
		t.skills[sk.Name] = sk
		t.names = append(t.names, sk.Name)
	}
	sort.Strings(t.names)
	return t
}

// SkillNames 返回已加载技能名（测试与自省用）。
func (t *UseSkillTool) SkillNames() []string { return append([]string(nil), t.names...) }

func (t *UseSkillTool) Definition() entity.ToolDefinition {
	return entity.ToolDefinition{
		Type: "function",
		Function: entity.FunctionSpec{
			Name: "use_skill",
			Description: "按需加载一份工程技能（SKILL.md 正文）：架构设计、代码模式、安全红线、" +
				"部署门禁等成熟工程经验。系统提示词的「工程技能库」清单列出了全部技能及其触发时机；" +
				"任务命中触发时机时，动手前先调用本工具加载对应技能。参数 name 为技能名；" +
				"传 \"list\" 返回技能清单。技能正文引用的验证脚本位于返回内容标注的目录下，用 bash 运行。" +
				"失败形态：技能名不存在时返回可用清单。",
			Parameters: schema(map[string]any{
				"name": strProp(`技能名（工程技能库清单中的名字），或 "list" 查看全部技能`),
			}, "name"),
		},
	}
}

// ConcurrencySafe：只读加载，可与其他工具并行。
func (t *UseSkillTool) ConcurrencySafe() bool { return true }

func (t *UseSkillTool) Execute(_ context.Context, argsJSON string, tctx *port.ToolContext) entity.ToolResult {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return entity.ToolResult{IsError: true, Content: "参数 JSON 解析失败: " + err.Error()}
	}
	if args.Name == "" || args.Name == "list" {
		var b strings.Builder
		b.WriteString("可用技能（用 use_skill 加载正文）：\n")
		for _, n := range t.names {
			fmt.Fprintf(&b, "- %s：%s\n", n, t.skills[n].Description)
		}
		return entity.ToolResult{Content: b.String()}
	}
	sk, ok := t.skills[args.Name]
	if !ok {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf(
			"技能 %q 不存在。可用技能：%s", args.Name, strings.Join(t.names, ", "))}
	}

	content := fmt.Sprintf("# 技能：%s\n# 目录：%s（正文引用的脚本在其 scripts/ 子目录，用 bash 运行，如 python3 %s/scripts/xxx.py）\n\n%s",
		sk.Name, sk.Dir, sk.Dir, sk.Body)
	content = TruncateOutput(content, t.maxOut, func() string {
		if tctx != nil && tctx.Spill != nil {
			return tctx.Spill("use_skill", content)
		}
		return ""
	})
	return entity.ToolResult{Content: content}
}
