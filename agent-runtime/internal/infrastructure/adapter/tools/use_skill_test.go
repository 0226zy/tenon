package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0226zy/protoforge/agent-runtime/internal/domain/entity"
	"github.com/0226zy/protoforge/agent-runtime/internal/domain/port"
)

func skillTool(skills ...entity.Skill) *UseSkillTool {
	return NewUseSkillTool(skills, 30000)
}

func TestUseSkillList(t *testing.T) {
	tool := skillTool(
		entity.Skill{Name: "arch-design", Description: "设计引导", Body: "正文", Dir: "/tmp/arch-design"},
		entity.Skill{Name: "backend-patterns", Description: "后端模式", Body: "正文", Dir: "/tmp/backend-patterns"},
	)
	res := tool.Execute(context.Background(), `{"name":"list"}`, nil)
	if res.IsError {
		t.Fatalf("list 不应报错: %s", res.Content)
	}
	if !strings.Contains(res.Content, "arch-design") || !strings.Contains(res.Content, "backend-patterns") {
		t.Fatalf("list 应含全部技能: %q", res.Content)
	}
	// 空 name 等价 list
	res2 := tool.Execute(context.Background(), `{}`, nil)
	if res2.IsError || !strings.Contains(res2.Content, "arch-design") {
		t.Fatal("空 name 应返回清单")
	}
}

func TestUseSkillLoad(t *testing.T) {
	tool := skillTool(entity.Skill{Name: "arch-design", Description: "设计引导", Body: "五阶段设计流程", Dir: "/tmp/arch-design"})
	res := tool.Execute(context.Background(), `{"name":"arch-design"}`, nil)
	if res.IsError {
		t.Fatalf("加载失败: %s", res.Content)
	}
	if !strings.Contains(res.Content, "五阶段设计流程") {
		t.Fatal("应返回技能正文")
	}
	if !strings.Contains(res.Content, "/tmp/arch-design") {
		t.Fatal("应标注技能目录（脚本定位用）")
	}
	if !tool.ConcurrencySafe() {
		t.Fatal("use_skill 应声明并发安全（只读）")
	}
}

func TestUseSkillNotFound(t *testing.T) {
	tool := skillTool(entity.Skill{Name: "arch-design", Description: "设计引导", Body: "正文", Dir: "/tmp/x"})
	res := tool.Execute(context.Background(), `{"name":"nope"}`, nil)
	if !res.IsError {
		t.Fatal("未知技能应报错")
	}
	if !strings.Contains(res.Content, "arch-design") {
		t.Fatal("报错应附可用技能清单（供模型自我纠正）")
	}
	// 参数畸形也是普通工具错误（错误即输入）
	res = tool.Execute(context.Background(), `{bad`, nil)
	if !res.IsError {
		t.Fatal("畸形 JSON 应返回工具错误")
	}
}

func TestUseSkillTruncate(t *testing.T) {
	big := strings.Repeat("长正文。", 5000)
	tool := NewUseSkillTool([]entity.Skill{{Name: "big", Description: "d", Body: big, Dir: "/tmp/big"}}, 1000)
	var spillPath string
	tctx := &port.ToolContext{
		Spill: func(toolName, content string) string {
			p := filepath.Join(t.TempDir(), toolName+".log")
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			spillPath = p
			return p
		},
	}
	res := tool.Execute(context.Background(), `{"name":"big"}`, tctx)
	if res.IsError {
		t.Fatal(res.Content)
	}
	if len(res.Content) > 1200 {
		t.Fatalf("超长正文应被截断，实际 %d 字节", len(res.Content))
	}
	if spillPath == "" || !strings.Contains(res.Content, spillPath) {
		t.Fatal("截断应经 spill 落盘并告知路径")
	}
}
