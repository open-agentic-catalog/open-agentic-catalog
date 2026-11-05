// Key lifecycle tests use independent codecs and controlled issuer discovery.
package web

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/securecookie"
)

// rotationKeyOracle never uses the server's codecs or multi-key decoder.
func rotationKeyOracle(t *testing.T, pair keyPair, cookie *http.Cookie, valid bool) {
	t.Helper()
	hash, err := hex.DecodeString(pair.Hash)
	if err != nil {
		t.Fatal(err)
	}
	block, err := hex.DecodeString(pair.Block)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	err = securecookie.New(hash, block).SetSerializer(securecookie.JSONEncoder{}).MaxAge(28800).Decode(cookie.Name, cookie.Value, &value)
	if (err == nil) != valid {
		t.Fatal("cookie signing key differs from independent single-key oracle")
	}
}

func writeRotationRing(t *testing.T, c Config, pairs ...keyPair) {
	t.Helper()
	raw, err := json.Marshal(pairs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.KeysFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionAndPendingLoginStagedKeyLifecycle(t *testing.T) {
	for _, seconds := range []int{60, 3600} {
		t.Run((time.Duration(seconds) * time.Second).String(), func(t *testing.T) {
			c := oidcFixtureConfig(t, "https://api.example.test", unitOIDCIssuer)
			c.SessionSeconds = seconds
			old := keyPair{Hash: strings.Repeat("ab", 64), Block: strings.Repeat("cd", 32)}
			next := keyPair{Hash: strings.Repeat("ef", 64), Block: strings.Repeat("12", 32)}
			// Codec timestamps stay at wall-clock now. Only application expiry advances.
			at := time.Now().UTC().Truncate(time.Second)
			host := func(pairs ...keyPair) *Server {
				writeRotationRing(t, c, pairs...)
				s := directHost(t, c)
				s.now = func() time.Time { return at }
				s.login.client.Transport = hostRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != unitOIDCIssuer+"/.well-known/openid-configuration" {
						t.Fatal("unexpected issuer contact")
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(unitOIDCDiscovery))}, nil
				})
				return s
			}
			writer := func(s *Server, want keyPair, other keyPair) []*http.Cookie {
				w := uiRequest(s, "POST", "/auth/login", "{}")
				if w.Code != 200 || len(w.Result().Cookies()) != 1 {
					t.Fatal("pending login writer failed")
				}
				cookies := []*http.Cookie{directHostSession(t, s), w.Result().Cookies()[0]}
				for _, cookie := range cookies {
					rotationKeyOracle(t, want, cookie, true)
					rotationKeyOracle(t, other, cookie, false)
				}
				return cookies
			}
			accept := func(s *Server, cookies []*http.Cookie, valid bool) {
				r := httptest.NewRequestWithContext(t.Context(), "GET", c.Origin+"/auth/session", nil)
				r.AddCookie(cookies[0])
				got, err := s.readSession(r)
				if (err == nil) != valid || (valid && (got.Token != "test-token" || got.Binding != s.binding)) {
					t.Fatal("session lifecycle admission differs")
				}
				id := strings.TrimPrefix(cookies[1].Name, c.CookieName+"-login-")
				w := uiRequest(s, "GET", "/auth/callback?state="+id+"&error=access_denied", "", cookies[1])
				status, code := 400, "invalid_login_state"
				if valid {
					status, code = 401, "login_not_completed"
				}
				assertUIProblem(t, w, status, code)
				if valid && (len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge != -1) {
					t.Fatal("accepted pending state not cleared")
				}
				if !valid && len(w.Result().Cookies()) != 0 {
					t.Fatal("invalid pending state changed cookies")
				}
			}
			original := host(old)
			saved := writer(original, old, next)
			staged := host(old, next)
			stagedCookies := writer(staged, old, next)
			accept(original, stagedCookies, true)
			accept(staged, saved, true)
			// Updating the file never changes an already started host's writer.
			promoted := host(next, old)
			accept(promoted, saved, true)
			fresh := writer(promoted, next, old)
			accept(staged, fresh, true)
			accept(original, fresh, false)
			at = at.Add(20 * time.Second)
			lastOld := writer(staged, old, next)
			overlap := host(next, old)
			accept(overlap, lastOld, true)
			accept(overlap, fresh, true)
			retired := host(next)
			accept(overlap, saved, true)
			accept(retired, saved, false)
			accept(retired, fresh, true)
			// Routine retirement starts at the LAST old writer, not the first rollout.
			lastWrite := at
			window := time.Duration(max(seconds, 300)) * time.Second
			at = lastWrite.Add(window - 20*time.Second)
			// At the first writer's retirement deadline the last writer is still live.
			if seconds >= 300 {
				r := httptest.NewRequestWithContext(t.Context(), "GET", c.Origin+"/auth/session", nil)
				r.AddCookie(lastOld[0])
				if _, err := overlap.readSession(r); err != nil {
					t.Fatal("retirement measured from first writer")
				}
			} else {
				id := strings.TrimPrefix(lastOld[1].Name, c.CookieName+"-login-")
				w := uiRequest(overlap, "GET", "/auth/callback?state="+id+"&error=access_denied", "", lastOld[1])
				assertUIProblem(t, w, 401, "login_not_completed")
			}
			at = lastWrite.Add(window - time.Second)
			// Inspect each absolute lifetime separately when session and state differ.
			r := httptest.NewRequestWithContext(t.Context(), "GET", c.Origin+"/auth/session", nil)
			r.AddCookie(lastOld[0])
			_, err := overlap.readSession(r)
			if (err == nil) != (seconds >= 300) {
				t.Fatal("session lifetime boundary differs")
			}
			id := strings.TrimPrefix(lastOld[1].Name, c.CookieName+"-login-")
			w := uiRequest(overlap, "GET", "/auth/callback?state="+id+"&error=access_denied", "", lastOld[1])
			status := 400
			if seconds <= 300 {
				status = 401
			}
			if w.Code != status {
				t.Fatal("pending lifetime boundary differs")
			}
			at = lastWrite.Add(window)
			accept(overlap, lastOld, false)
			// Still valid codec signatures isolate absolute expiry from codec wall time.
			for _, cookie := range lastOld {
				rotationKeyOracle(t, old, cookie, true)
			}
			current := writer(retired, next, old)
			accept(retired, current, true)
		})
	}
}
