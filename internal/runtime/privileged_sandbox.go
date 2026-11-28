package runtime

import (
	"context"
	"errors"
	"fmt"
)

type privilegedSandboxSettingKey struct{}
type privilegedSandboxSettingLookup func(context.Context) (bool, error)

// WithPrivilegedSandboxSetting carries the execution machine's trusted policy
// lookup through sandbox preparation.
func WithPrivilegedSandboxSetting(ctx context.Context, lookup func(context.Context) (bool, error)) context.Context {
	return context.WithValue(ctx, privilegedSandboxSettingKey{}, privilegedSandboxSettingLookup(lookup))
}

func checkPrivilegedSandbox(ctx context.Context, lookup privilegedSandboxSettingLookup) error {
	if lookup != nil {
		allowed, err := lookup(ctx)
		if err != nil {
			return fmt.Errorf("读取执行机器的特权 Docker 沙箱配置失败: %w", err)
		}
		if allowed {
			return nil
		}
	}
	return errors.New("当前执行机器的特权 Docker 沙箱已关闭，请在「设置 → 执行机器」中开启")
}
