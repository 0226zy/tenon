package config

import (
	"strings"
	"testing"
)

// 部署前置检查（评审 P0-3）：非回环监听 + 空鉴权令牌必须拒绝启动。
func TestCheckDeployment(t *testing.T) {
	cases := []struct {
		name      string
		listen    string
		authToken string
		wantErr   bool
	}{
		{"回环无令牌放行", "127.0.0.1:3080", "", false},
		{"localhost 无令牌放行", "localhost:3080", "", false},
		{"IPv6 回环无令牌放行", "[::1]:3080", "", false},
		{"全接口无令牌拒绝", "0.0.0.0:3080", "", true},
		{"空主机（等同全接口）无令牌拒绝", ":3080", "", true},
		{"公网地址无令牌拒绝", "203.0.113.10:3080", "", true},
		{"全接口有令牌放行", "0.0.0.0:3080", "secret", false},
		{"公网地址有令牌放行", "203.0.113.10:3080", "secret", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.Server.Listen = c.listen
			cfg.Server.AuthToken = c.authToken
			err := CheckDeployment(cfg)
			if c.wantErr && err == nil {
				t.Fatalf("listen=%q 无令牌应拒绝启动", c.listen)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("listen=%q 不应拒绝: %v", c.listen, err)
			}
			if c.wantErr && !strings.Contains(err.Error(), "auth_token") {
				t.Fatalf("错误信息应指向 auth_token 配置: %v", err)
			}
		})
	}
}
