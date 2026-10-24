// Package derr 定义跨层判别的哨兵错误。跨层判断用 errors.Is，绝不用字符串比较。
package derr

import "errors"

var (
	// ErrMaxIterations 达到最大迭代轮数（全局保护性终止之一）。
	ErrMaxIterations = errors.New("达到最大迭代轮数，任务未完成")
	// ErrBudgetExhausted 会话 token 预算耗尽（全局保护性终止之二）。
	ErrBudgetExhausted = errors.New("会话 token 预算耗尽")
	// ErrLivelock 检测到无进展循环（重复调用指纹超阈值，强制打断）。
	ErrLivelock = errors.New("检测到无进展循环：相同工具调用反复出现")
	// ErrCircuitOpen 恢复路径熔断：连续失败达到阈值，放弃自动恢复。
	ErrCircuitOpen = errors.New("熔断器打开：连续失败达到阈值")
	// ErrNotRetryable 不可重试错误（参数非法 / 权限不足 / 认证失败），必须改变输入或策略。
	ErrNotRetryable = errors.New("不可重试错误")
	// ErrSessionNotFound 会话不存在。
	ErrSessionNotFound = errors.New("会话不存在")
	// ErrSessionBusy 会话已有正在运行的回合（一个会话同一时刻只跑一个 turn）。
	ErrSessionBusy = errors.New("会话正忙：已有运行中的回合")
	// ErrApprovalTimeout 审批超时未决。
	ErrApprovalTimeout = errors.New("审批超时：按拒绝处理")
	// ErrNoPendingApproval 审批不存在或已裁决。
	ErrNoPendingApproval = errors.New("审批不存在或已裁决")
	// ErrForkPointInvalid fork 切点非法。
	ErrForkPointInvalid = errors.New("fork 切点非法")
	// ErrRunNotFound Run 不存在于指定会话。
	ErrRunNotFound = errors.New("Run 不存在")
	// ErrRunIDConflict 同 run_id 对应了不同 payload hash。
	ErrRunIDConflict = errors.New("run_id 已被不同请求占用")
	// ErrStaleGeneration generation 小于会话已经接受的最高值。
	ErrStaleGeneration = errors.New("generation 已过期")
	// ErrModelNotFound 模型不在只读目录中。
	ErrModelNotFound = errors.New("模型不存在")
	// ErrInvalidRun Run 标识、hash 或内容不符合契约。
	ErrInvalidRun = errors.New("Run 请求非法")
)
