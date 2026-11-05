// Cookie encoding binds sessions and pending logins to one configured deployment.

package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/securecookie"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// session retains no grants: the Catalog resolves current authorization on
// every request. Expiry is absolute; requests never extend the token lifetime.
type session struct {
	Binding string `json:"b"`
	Token   string `json:"t"`
	Expires int64  `json:"e"`
}

// initCookies loads the immutable rotation ring and computes its deployment binding.
func (s *Server) initCookies() error {
	var pairs []keyPair
	if err := readJSONFile(s.config.KeysFile, &pairs); err != nil || len(pairs) < 1 || len(pairs) > 3 {
		return safeerr.New("UI keysFile must contain one to three key pairs")
	}
	for _, pair := range pairs {
		hash, err := decodeKey(pair.Hash, 64)
		if err != nil {
			return err
		}
		block, err := decodeKey(pair.Block, 32)
		if err != nil {
			return err
		}
		// The protocol state has a five-minute lifetime even when the access
		// session is shorter. Each decoded value checks its own absolute expiry.
		s.codecs = append(s.codecs, securecookie.New(hash, block).SetSerializer(securecookie.JSONEncoder{}).MaxAge(max(s.config.SessionSeconds, 300)).MaxLength(3600))
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{s.config.Origin, s.config.BasePath, s.config.Upstream, s.config.CookieName, s.config.AuthMode, s.config.Issuer, s.config.ClientID}, "\x00")))
	s.binding = hex.EncodeToString(digest[:])
	return nil
}

// readSession requires one authentic, unexpired cookie for this deployment.
func (s *Server) readSession(r *http.Request) (session, error) {
	var value session
	cookies := r.CookiesNamed(s.config.CookieName)
	if len(cookies) != 1 {
		return value, safeerr.New("UI session required")
	}
	if err := securecookie.DecodeMulti(s.config.CookieName, cookies[0].Value, &value, s.codecs...); err != nil || value.Binding != s.binding || value.Expires <= s.now().Unix() || !validToken(value.Token) {
		return session{}, safeerr.New("UI session expired or invalid")
	}
	return value, nil
}

// setSession caps expiry by the deployment lifetime and writes with the first key.
func (s *Server) setSession(w http.ResponseWriter, token string, expiry time.Time) error {
	if !validToken(token) || !expiry.After(s.now()) {
		return safeerr.New("login did not provide a usable access token")
	}
	maximum := s.now().Add(time.Duration(s.config.SessionSeconds) * time.Second)
	if expiry.After(maximum) {
		expiry = maximum
	}
	encoded, err := s.codecs[0].Encode(s.config.CookieName, session{Binding: s.binding, Token: token, Expires: expiry.Unix()})
	if err != nil {
		return safeerr.New("access token exceeds the supported session cookie size")
	}
	http.SetCookie(w, s.cookie(s.config.CookieName, encoded, expiry))
	return nil
}

// cookie applies the common path and browser protections to sessions and login state.
func (s *Server) cookie(name, value string, expiry time.Time) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: s.config.BasePath + "/", Secure: s.secure, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: expiry, MaxAge: max(1, int(expiry.Sub(s.now()).Seconds()))} // #nosec G124 -- Secure is false only for validated loopback HTTP development, never for a production origin.
}

// clearCookie expires the named cookie using the same deployment path and attributes.
func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	c := s.cookie(name, "", time.Unix(1, 0)) // #nosec G124 -- Deletion preserves the same validated Secure/HttpOnly/SameSite attributes.
	c.MaxAge = -1
	http.SetCookie(w, c)
}

// validToken checks header-safe syntax and size; the Catalog authenticates the token.
func validToken(token string) bool {
	if token == "" || len(token) > 16<<10 {
		return false
	}
	for _, r := range token {
		if r <= 32 || r >= 127 {
			return false
		}
	}
	return true
}
