package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 模板渲染：参数转义、缺参数拒绝、渲染确定性。
func TestRenderDeployCommand(t *testing.T) {
	target := DeployTarget{
		Name:            "docker-prod",
		CommandTemplate: "docker build -t reg/{{image}}:{{tag}} . && docker push reg/{{image}}:{{tag}}",
	}

	// 正常渲染：值被单引号包裹
	cmd, err := renderDeployCommand(target, map[string]string{"image": "myapp", "tag": "v1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "docker build -t reg/'myapp':'v1.0' . && docker push reg/'myapp':'v1.0'" {
		t.Fatalf("渲染结果不符: %q", cmd)
	}

	// 注入尝试：命令替换被单引号中和，不可能逃逸
	cmd, err = renderDeployCommand(target, map[string]string{"image": "$(rm -rf /)", "tag": "'; rm -rf /; '"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, `'$(rm -rf /)'`) {
		t.Fatalf("注入值未被单引号包裹: %q", cmd)
	}
	if !strings.Contains(cmd, `''\''; rm -rf /; '\'''`) {
		t.Fatalf("单引号未正确转义: %q", cmd)
	}

	// 缺参数：残留占位符即拒绝
	if _, err := renderDeployCommand(target, map[string]string{"image": "x"}); err == nil {
		t.Fatal("缺参数应报错")
	}
}

// Execute：成功部署 / 未知 target 列出可用目标。
func TestDeployToolExecute(t *testing.T) {
	dir := t.TempDir()
	bash := NewBashTool(30*time.Second, 30000, func(string) string { return dir }, nil)
	t.Cleanup(func() { bash.CloseSession("t") })
	tool := NewDeployTool(bash, []DeployTarget{
		{Name: "local-echo", Description: "本地模拟部署", CommandTemplate: "echo deploying {{image}}:{{tag}}"},
	}, 60*time.Second)

	res := tool.Execute(context.Background(), `{"target":"local-echo","params":{"image":"app","tag":"v2"}}`, testCtx(dir))
	if res.IsError {
		t.Fatalf("部署成功用例报错: %s", res.Content)
	}
	if !strings.Contains(res.Content, "✓ 部署完成") || !strings.Contains(res.Content, "deploying app:v2") {
		t.Fatalf("结果缺少语义前缀或输出: %q", res.Content)
	}

	res = tool.Execute(context.Background(), `{"target":"nonexistent","params":{}}`, testCtx(dir))
	if !res.IsError || !strings.Contains(res.Content, "local-echo") {
		t.Fatalf("未知 target 应报错并列出可用目标: %+v", res)
	}

	// 部署失败（命令非零退出）错误回注
	tool2 := NewDeployTool(bash, []DeployTarget{
		{Name: "bad", Description: "总是失败", CommandTemplate: "(echo push rejected; exit 1)"},
	}, 60*time.Second)
	res = tool2.Execute(context.Background(), `{"target":"bad","params":{}}`, testCtx(dir))
	if !res.IsError || !strings.Contains(res.Content, "部署失败") || !strings.Contains(res.Content, "push rejected") {
		t.Fatalf("失败输出应回注: %+v", res)
	}
}
