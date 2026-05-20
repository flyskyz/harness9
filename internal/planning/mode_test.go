package planning_test

import (
	"testing"

	"github.com/harness9/internal/planning"
)

func TestPlanMode_Next_Cycles(t *testing.T) {
	if planning.PlanModeDefault.Next() != planning.PlanModePlan {
		t.Error("Default.Next() should be Plan")
	}
	if planning.PlanModePlan.Next() != planning.PlanModeAutoEdit {
		t.Error("Plan.Next() should be AutoEdit")
	}
	if planning.PlanModeAutoEdit.Next() != planning.PlanModeDefault {
		t.Error("AutoEdit.Next() should wrap to Default")
	}
}

func TestPlanMode_String(t *testing.T) {
	cases := []struct {
		mode planning.PlanMode
		want string
	}{
		{planning.PlanModeDefault, "DEFAULT"},
		{planning.PlanModePlan, "PLAN"},
		{planning.PlanModeAutoEdit, "AUTO"},
	}
	for _, tc := range cases {
		if got := tc.mode.String(); got != tc.want {
			t.Errorf("mode %d String() = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestPlanMode_Label(t *testing.T) {
	if got := planning.PlanModeDefault.Label(); got != "" {
		t.Errorf("Default.Label() = %q, want empty", got)
	}
	if got := planning.PlanModePlan.Label(); got != "[PLAN]" {
		t.Errorf("Plan.Label() = %q, want [PLAN]", got)
	}
	if got := planning.PlanModeAutoEdit.Label(); got != "[AUTO (未实现)]" {
		t.Errorf("AutoEdit.Label() = %q, want [AUTO (未实现)]", got)
	}
}
