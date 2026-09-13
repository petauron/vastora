package agent

import (
	"context"
	"errors"
	"testing"
)

func TestRuntimeCleanupStopsAtFirstFailure(t *testing.T) {
	for _, mode := range []string{"command-error", "authorization-error", "cancelled-command", "cancelled-authorization", "success"} {
		for failedAt := 0; failedAt < 9; failedAt++ {
			t.Run(mode+string(rune('0'+failedAt)), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls, grants := 0, 0
				failure := errors.New("injected cleanup error")
				steps := make([]runtimeCleanupStep, 9)
				for i := range steps {
					steps[i] = runtimeCleanupStep{"fixture", func(context.Context) error {
						calls++
						if i == failedAt {
							if mode == "command-error" {
								return failure
							}
							if mode == "cancelled-command" {
								cancel()
							}
						}
						return nil
					}}
				}
				authorize := func(context.Context, string) error {
					index := grants
					grants++
					if index == failedAt {
						if mode == "authorization-error" {
							return failure
						}
						if mode == "cancelled-authorization" {
							cancel()
						}
					}
					return nil
				}
				err := runRuntimeCleanupSteps(ctx, authorize, steps)
				wantCalls, wantGrants := failedAt+1, failedAt+1
				if mode == "authorization-error" || mode == "cancelled-authorization" {
					wantCalls--
				}
				if mode == "success" {
					wantCalls, wantGrants = 9, 9
				}
				if calls != wantCalls || grants != wantGrants || (err == nil) != (mode == "success") {
					t.Fatalf("calls=%d grants=%d err=%v", calls, grants, err)
				}
				if mode == "command-error" || mode == "authorization-error" {
					if !errors.Is(err, failure) {
						t.Fatal(err)
					}
				}
				if mode == "cancelled-command" || mode == "cancelled-authorization" {
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
