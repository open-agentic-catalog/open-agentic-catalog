package anstrust

import "testing"

// BenchmarkVerifyNative measures complete bounded offline verification, excluding
// fixture loading and key parsing. It makes no network or database calls.
func BenchmarkVerifyNative(b *testing.B) {
	for _, version := range []string{"v1", "v2"} {
		b.Run(version, func(b *testing.B) {
			verifier, bundle := native(b, version)
			if !assess(b, verifier, bundle, fixtureTime).CurrentRegistration {
				b.Fatal("invalid benchmark control")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				r, err := verifier.Verify(b.Context(), bundle, fixtureTime)
				if err != nil || !r.CurrentRegistration {
					b.Fatal("verification failed", err)
				}
			}
		})
	}
}
