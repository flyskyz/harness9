// Package hooks 提供通用的双向工具拦截器机制（Hooks）。
//
// HookRegistry 实现 tools.Registry 接口，在工具执行前后调用注册的 ToolHook 链。
// BeforeExecute 正向执行；AfterExecute 逆向执行（洋葱模型）。
// BeforeExecute 返回 error 时立即短路，跳过内层执行和所有 AfterExecute。
package hooks

import (
	"context"

	"github.com/harness9/internal/schema"
	"github.com/harness9/internal/tools"
)

// ToolHook 是双向工具拦截器接口。
// BeforeExecute 在工具调用前触发，返回 error 时短路整个调用链。
// AfterExecute 在工具调用后触发，可修改返回的 ToolResult。
type ToolHook interface {
	BeforeExecute(ctx context.Context, tc schema.ToolCall) (context.Context, error)
	AfterExecute(ctx context.Context, tc schema.ToolCall, result schema.ToolResult) schema.ToolResult
}

// HookRegistry 用 hook 链包装原始 Registry，实现 tools.Registry 接口。
// 零 hook 时行为与原始 Registry 完全一致。
type HookRegistry struct {
	inner tools.Registry
	hooks []ToolHook
}

// NewHookRegistry 创建包装 inner 的 HookRegistry，依次应用给定的 hooks。
func NewHookRegistry(inner tools.Registry, hooks ...ToolHook) *HookRegistry {
	return &HookRegistry{inner: inner, hooks: hooks}
}

// Register 直接委托给内层 Registry。
func (r *HookRegistry) Register(tool tools.BaseTool) error {
	return r.inner.Register(tool)
}

// GetAvailableTools 直接委托给内层 Registry。
func (r *HookRegistry) GetAvailableTools() []schema.ToolDefinition {
	return r.inner.GetAvailableTools()
}

// Execute 按洋葱模型依次执行 hook 链，中间调用内层 Registry.Execute。
// 任何 BeforeExecute 返回 error 时立即短路，不调用内层也不调用 AfterExecute。
func (r *HookRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	for _, h := range r.hooks {
		newCtx, err := h.BeforeExecute(ctx, call)
		if err != nil {
			return schema.ToolResult{
				ToolCallID: call.ID,
				Output:     err.Error(),
				IsError:    true,
			}
		}
		ctx = newCtx
	}

	result := r.inner.Execute(ctx, call)

	for i := len(r.hooks) - 1; i >= 0; i-- {
		result = r.hooks[i].AfterExecute(ctx, call, result)
	}
	return result
}
