package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validSkill = `---
name: arch-design
description: 架构设计与代码设计引导。
---

# 正文

设计流程五阶段……
`

func writeSkill(t *testing.T, root, dir, content string) {
	t.Helper()
	sub := filepath.Join(root, dir)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDir(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "arch-design", validSkill)
	writeSkill(t, root, "security-baseline", strings.Replace(validSkill, "arch-design", "security-baseline", 1))
	// 非 skill 目录（无 SKILL.md）与散文件应被跳过
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, err := LoadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("加载到 %d 个技能, 期望 2", len(skills))
	}
	// 按 name 排序（稳定）
	if skills[0].Name != "arch-design" || skills[1].Name != "security-baseline" {
		t.Fatalf("排序不稳定: %v", []string{skills[0].Name, skills[1].Name})
	}
	if !strings.Contains(skills[0].Body, "设计流程五阶段") {
		t.Fatal("正文应保留 frontmatter 之后的内容")
	}
	if strings.Contains(skills[0].Body, "description:") {
		t.Fatal("正文不应包含 frontmatter")
	}
	if !filepath.IsAbs(skills[0].Dir) {
		t.Fatal("Dir 应为绝对路径（脚本定位用）")
	}
}

func TestLoadDirMissing(t *testing.T) {
	skills, err := LoadDir(filepath.Join(t.TempDir(), "nonexistent"))
	if err != nil || skills != nil {
		t.Fatalf("目录不存在应返回空（不启用），得到 %v, %v", skills, err)
	}
}

func TestLoadDirBadSkill(t *testing.T) {
	cases := map[string]string{
		"no-frontmatter": "# 没有头部\n",
		"unclosed":       "---\nname: x\ndescription: y\n",
		"no-name":        "---\ndescription: y\n---\n正文\n",
		"no-desc":        "---\nname: bad\n---\n正文\n",
		"name-mismatch":  "---\nname: other\ndescription: y\n---\n正文\n",
		"empty-body":     "---\nname: bad\ndescription: y\n---\n",
	}
	for dir, content := range cases {
		t.Run(dir, func(t *testing.T) {
			root := t.TempDir()
			writeSkill(t, root, "bad", content)
			if _, err := LoadDir(root); err == nil {
				t.Fatal("损坏的 SKILL.md 应在启动期报错")
			}
		})
	}
}

func TestCatalog(t *testing.T) {
	if Catalog(nil) != "" {
		t.Fatal("无技能时目录应为空串（不拼进系统提示词）")
	}
	skills, err := LoadDir(func() string {
		root := t.TempDir()
		writeSkill(t, root, "arch-design", validSkill)
		return root
	}())
	if err != nil {
		t.Fatal(err)
	}
	cat := Catalog(skills)
	if !strings.Contains(cat, "arch-design") || !strings.Contains(cat, "架构设计与代码设计引导") {
		t.Fatalf("目录清单应含技能名与描述: %q", cat)
	}
	if !strings.Contains(cat, "use_skill") {
		t.Fatal("目录清单应指引模型用 use_skill 加载")
	}
}
