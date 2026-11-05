// Execution context carries bounded request and Job attribution into audit
// construction without retaining tokens, command keys or Job payloads.

package audit

import "context"

type executionKey struct{}
type jobExecutionKey struct{}

// WithExecution installs safe request attribution for downstream evidence.
func WithExecution(ctx context.Context, execution Execution) context.Context {
	return context.WithValue(ctx, executionKey{}, execution)
}

// ExecutionFrom returns the current safe execution attribution, if any.
func ExecutionFrom(ctx context.Context) Execution {
	execution, _ := ctx.Value(executionKey{}).(Execution)
	return execution
}

// WithCommandID adds the opaque protocol-v2 reservation identifier without
// retaining the client key, request digest, or execution token.
func WithCommandID(ctx context.Context, commandID string) context.Context {
	execution := ExecutionFrom(ctx)
	execution.CommandID = commandID
	return WithExecution(ctx, execution)
}

// WithJobExecution installs bounded worker-attempt attribution for domain
// evidence created by the handler. It deliberately carries no Job payload.
func WithJobExecution(ctx context.Context, execution JobExecution) context.Context {
	return context.WithValue(ctx, jobExecutionKey{}, execution)
}

// JobExecutionFrom returns the current worker-attempt attribution, if any.
func JobExecutionFrom(ctx context.Context) (JobExecution, bool) {
	execution, ok := ctx.Value(jobExecutionKey{}).(JobExecution)
	return execution, ok
}

// WithJobTrigger adds the validated code-owned trigger selected by a handler.
func WithJobTrigger(ctx context.Context, trigger string) context.Context {
	execution, ok := JobExecutionFrom(ctx)
	if !ok {
		return ctx
	}
	execution.Trigger = trigger
	return WithJobExecution(ctx, execution)
}
