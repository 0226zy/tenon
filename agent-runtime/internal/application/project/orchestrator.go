// Package project 是规划层编排用例（对齐全栈四层架构的 Planner 层）：
// 把「需求 → 规划 → 编码」编排为两个血缘相连的会话，复用现有三件地基——
//
//   - 角色化 = 系统提示词注入：规划会话以架构师角色提示词启动（v1 经首条
//     用户消息注入，零侵入；工具视图过滤留作后续增强）；
//   - 多模型分工 = LLMResolver 既有能力：规划会话与编码会话可选用不同模型；
//   - 血缘 = fork：编码会话从规划会话末尾 fork，需求与规划结论天然进入
//     编码上下文，parent_id 血缘可追溯。
//
// 编排不引入任何多智能体框架：它只是 application 层的一个新用例，
// 所需能力（会话管理、模型切换、回合驱动）全部复用既有端口与 Manager。
package project

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/0226zy/protoforge/agent-runtime/internal/application/session"
)

// 阶段。
const (
	PhasePlanning   = "planning"    // 规划回合进行中
	PhasePlanned    = "planned"     // 规划完成（未请求自动实施，终止于此）
	PhaseCoding     = "coding"      // 编码回合进行中
	PhaseDone       = "done"        // 编码完成
	PhasePlanFailed = "plan_failed" // 规划回合失败
	PhaseCodeFailed = "code_failed" // 编码回合失败
)

// Plan 是一次规划编排的状态（PlanID 即规划会话 ID）。
type Plan struct {
	PlanID        string `json:"plan_id"`
	CodeSessionID string `json:"code_session_id,omitempty"`
	Phase         string `json:"phase"`
	Err           string `json:"err,omitempty"`
}

// Request 是一次规划编排的输入。
type Request struct {
	Cwd           string `json:"cwd"`            // 工作区根（空 = 服务端当前目录）
	Requirement   string `json:"requirement"`    // 需求描述（必填）
	PlannerModel  string `json:"planner_model"`  // 规划会话模型（空 = 缺省）
	CoderModel    string `json:"coder_model"`    // 编码会话模型（空 = 沿用规划会话模型）
	Mode          string `json:"mode"`           // 审批模式（空 = 服务端缺省）
	AutoImplement bool   `json:"auto_implement"` // 规划完成后自动 fork 并启动编码会话
}

// Orchestrator 驱动「规划 → 编码」编排。并发安全。
type Orchestrator struct {
	manager *session.Manager
	// turnTimeout 是单个回合的等待上限（防止编排 goroutine 无限悬挂）。
	turnTimeout time.Duration
	// pollInterval 是回合结束的轮询间隔。
	pollInterval time.Duration

	mu    sync.Mutex
	plans map[string]*Plan // key = 规划会话 ID
}

// NewOrchestrator 构造编排器。
func NewOrchestrator(manager *session.Manager, turnTimeout time.Duration) *Orchestrator {
	if turnTimeout <= 0 {
		turnTimeout = 30 * time.Minute
	}
	return &Orchestrator{
		manager:      manager,
		turnTimeout:  turnTimeout,
		pollInterval: 300 * time.Millisecond,
		plans:        map[string]*Plan{},
	}
}

// Start 创建规划会话并在后台推进编排；立即返回初始状态（Phase=planning）。
func (o *Orchestrator) Start(req Request) (*Plan, error) {
	if strings.TrimSpace(req.Requirement) == "" {
		return nil, fmt.Errorf("requirement 不能为空")
	}
	rt, err := o.manager.Create(req.Cwd, req.Mode, "规划: "+firstLine(req.Requirement, 60), req.PlannerModel)
	if err != nil {
		return nil, err
	}
	plan := &Plan{PlanID: rt.ID(), Phase: PhasePlanning}
	o.mu.Lock()
	o.plans[plan.PlanID] = plan
	o.mu.Unlock()
	go o.run(plan, req)
	return plan, nil
}

// Get 查询编排状态（按规划会话 ID）。
func (o *Orchestrator) Get(planID string) (*Plan, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.plans[planID]
	if !ok {
		return nil, false
	}
	cp := *p
	return &cp, true
}

// run 后台推进：规划回合 →（可选）fork 编码会话并换模型 → 编码回合。
func (o *Orchestrator) run(plan *Plan, req Request) {
	fail := func(phase string, err error) {
		o.set(plan, phase, "", err)
	}

	if err := o.runTurn(plan.PlanID, plannerPrompt(req.Requirement)); err != nil {
		fail(PhasePlanFailed, err)
		return
	}
	if !req.AutoImplement {
		o.set(plan, PhasePlanned, "", nil)
		return
	}

	// 编码会话从规划末尾 fork：需求与规划结论完整继承，血缘可追溯。
	codeRt, err := o.manager.Fork(plan.PlanID, -1)
	if err != nil {
		fail(PhaseCodeFailed, fmt.Errorf("fork 编码会话失败: %w", err))
		return
	}
	codeID := codeRt.ID()
	if req.CoderModel != "" {
		if err := o.manager.SetModel(codeID, req.CoderModel); err != nil {
			fail(PhaseCodeFailed, fmt.Errorf("编码会话切换模型失败: %w", err))
			return
		}
	}
	o.set(plan, PhaseCoding, codeID, nil)

	if err := o.runTurn(codeID, implementPrompt); err != nil {
		fail(PhaseCodeFailed, err)
		return
	}
	o.set(plan, PhaseDone, codeID, nil)
}

// runTurn 在启动前记录日志末尾，避免 fork 继承的旧 turn_done 提前结束新回合。
func (o *Orchestrator) runTurn(id, prompt string) error {
	startSeq, err := o.manager.NextSeq(id)
	if err != nil {
		return err
	}
	if err := o.manager.RunTurn(id, prompt); err != nil {
		return err
	}
	return o.waitTurn(id, startSeq)
}

// set 更新编排状态。
func (o *Orchestrator) set(plan *Plan, phase, codeID string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	plan.Phase = phase
	if codeID != "" {
		plan.CodeSessionID = codeID
	}
	if err != nil {
		plan.Err = err.Error()
	}
}

// waitTurn 只扫描 startSeq 之后的事件，直到出现本回合的 turn_done / turn_failed marker。
func (o *Orchestrator) waitTurn(id string, startSeq int) error {
	deadline := time.Now().Add(o.turnTimeout)
	fromSeq := startSeq
	for time.Now().Before(deadline) {
		events, err := o.manager.Events(id, fromSeq)
		if err != nil {
			return err
		}
		for _, ev := range events {
			fromSeq = ev.Seq + 1
			if ev.Marker == nil {
				continue
			}
			switch ev.Marker.Type {
			case "turn_done":
				return nil
			case "turn_failed":
				return fmt.Errorf("回合失败: %s", ev.Marker.Detail)
			}
		}
		time.Sleep(o.pollInterval)
	}
	return fmt.Errorf("等待回合结束超时（%s）", o.turnTimeout)
}

// plannerPrompt 是规划师角色的首条消息（v1 角色化：提示词注入，零侵入）。
func plannerPrompt(requirement string) string {
	return `你是本项目的规划师（架构师角色）。本回合只做分析与设计：不要修改任何文件，
可以用 read_file / glob / grep / bash（只读命令）调研代码库现状。
任务：把以下需求拆解为可实施的方案，输出结构化实施计划，包括：
1. 目标概述与验收标准（如何判定完成，通常以 run_tests 全绿为准）；
2. 技术选型（如涉及）与理由；
3. 模块/文件级划分：要新建或修改哪些文件，各自职责；
4. 实施步骤（有序、小步、每步可验证）。
后续会有编码会话从本会话 fork 并严格按此计划实施，计划必须具体、可执行。

需求：
` + requirement
}

// implementPrompt 是编码会话的首条消息。
const implementPrompt = `请严格按以上规划实施。纪律：
1. 小步修改：按规划步骤逐步推进，不偏离计划；
2. 每完成一个有意义的步骤即运行 run_tests 验证，失败立即修复再继续；
3. 全部完成后再次运行 run_tests 确认全绿；
4. 最后总结：改动清单、验证结果、与规划的偏差（如有）。`

// firstLine 取首行并限长（会话标题用）。
func firstLine(s string, max int) string {
	line := strings.TrimSpace(s)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if len(line) > max {
		line = line[:max] + "…"
	}
	if line == "" {
		line = "（未命名需求）"
	}
	return line
}
