package authn

import (
	"context"
	"sync/atomic"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type oidcVerificationKey struct{}
type oidcVerificationState struct{ dependency atomic.Bool }

// The OIDC verifier converts signature errors to text. Record only a typed
// dependency classification before that conversion, in this caller's context.
// No token, raw error or state from another request is retained here.
type classifiedOIDCKeys struct{ keys oidc.KeySet }

func (k classifiedOIDCKeys) VerifySignature(ctx context.Context, token string) ([]byte, error) {
	result, err := k.keys.VerifySignature(ctx, token)
	if err != nil && identity.ErrorCodeOf(err) == identity.CodeDependency {
		if state, ok := ctx.Value(oidcVerificationKey{}).(*oidcVerificationState); ok {
			state.dependency.Store(true)
		}
	}
	return result, err
}
func (o *OIDC) verify(ctx context.Context, token string) (*oidc.IDToken, error) {
	state := &oidcVerificationState{}
	result, err := o.verifyCached(context.WithValue(ctx, oidcVerificationKey{}, state), token)
	if err != nil && (state.dependency.Load() || ctx.Err() != nil) {
		return nil, identity.E(identity.CodeDependency, "OIDC verification is temporarily unavailable")
	}
	return result, err
}
