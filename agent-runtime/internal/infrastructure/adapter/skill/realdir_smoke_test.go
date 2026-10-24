package skill

import "testing"

// 冒烟：真实 skills/ 目录（仓库内置资产）必须全部可解析。
func TestRealSkillsDirSmoke(t *testing.T) {
	skills, err := LoadDir("../../../../skills")
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) < 9 {
		t.Fatalf("内置技能应 >=9 个, 实际 %d", len(skills))
	}
	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
		if s.Description == "" || s.Body == "" || s.Dir == "" {
			t.Fatalf("技能 %s 字段不完整", s.Name)
		}
	}
	for _, want := range []string{"arch-design", "product-scope", "fullstack-architecture", "backend-patterns", "frontend-react", "frontend-vue", "security-baseline", "deploy-ship", "debug-playbook"} {
		if !names[want] {
			t.Fatalf("缺少内置技能 %s", want)
		}
	}
	t.Logf("目录清单长度: %d 字符", len(Catalog(skills)))
}
