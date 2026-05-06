package app

import "strings"

const (
	workflowIdle              = "idle"
	workflowInterview         = "interview"
	workflowResearch          = "research"
	workflowReview            = "review"
	workflowBuildPlan         = "build_plan"
	workflowPlanApproval      = "plan_approval"
	workflowExecuting         = "executing"
	workflowBlockedQuestion   = "blocked_question"
	workflowBlockedPermission = "blocked_permission"
)

func (m *Model) setWorkflowPhase(phase, detail string) {
	phase = strings.TrimSpace(phase)
	detail = strings.TrimSpace(detail)
	if phase == "" || phase == workflowIdle {
		m.workflowPhase = ""
		m.workflowDetail = ""
		return
	}
	m.workflowPhase = phase
	m.workflowDetail = detail
}

func (m *Model) workflowLabel() string {
	if m == nil || m.workflowPhase == "" {
		return ""
	}
	label := m.workflowPhase
	switch m.workflowPhase {
	case workflowInterview:
		label = "planning: questions"
	case workflowResearch:
		label = "planning: research"
	case workflowReview:
		label = "planning: review"
	case workflowBuildPlan:
		label = "planning: build"
	case workflowPlanApproval:
		label = "planning: approval"
	case workflowExecuting:
		label = "executing plan"
	case workflowBlockedQuestion:
		label = "blocked: question"
	case workflowBlockedPermission:
		label = "blocked: permission"
	}
	if m.workflowDetail != "" {
		return label + " - " + m.workflowDetail
	}
	return label
}
