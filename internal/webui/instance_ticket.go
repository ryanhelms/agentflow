package webui

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const launchTTL = 2 * time.Minute

func signature(key, purpose, challenge string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(purpose + "\x00" + challenge))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// The port can be recycled after a crash. Prove the listener has the key in
// the private instance record. Neither the key nor a browser ticket is sent.
func (i instance) proveInstance(w http.ResponseWriter, r *http.Request) {
	nonce := r.Header.Get("X-Devflow-Challenge")
	if len(nonce) != 43 || r.Header.Get("Origin") != "" || i.Key == "" ||
		!hmac.Equal([]byte(r.Header.Get("X-Devflow-Proof")), []byte(signature(i.Key, "client/v1", nonce))) {
		http.Error(w, "invalid instance challenge", http.StatusBadRequest)
		return
	}
	reply(w, http.StatusOK, signature(i.Key, "instance/v1", nonce))
}

func mintLaunch(key string, now time.Time) string {
	nonce := randomKey()
	payload := strconv.FormatInt(now.Add(launchTTL).Unix(), 10) + "." + nonce
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "." + signature(key, "launch/v1", payload)))
}

func (s *server) consumeLaunch(token string) bool {
	if s.instanceKey == "" || len(token) > 205 {
		return false
	}
	ticket, proof, scoped := strings.Cut(token, ".")
	if len(ticket) > 160 || (scoped && len(proof) != 43) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(ticket)
	if err != nil {
		return false
	}
	stamp, rest, ok := strings.Cut(string(raw), ".")
	if !ok {
		return false
	}
	nonce, mac, ok := strings.Cut(rest, ".")
	until, err := strconv.ParseInt(stamp, 10, 64)
	now := time.Now().Unix()
	if !ok || len(nonce) != 43 || err != nil || until < now || until > now+int64(launchTTL.Seconds()) ||
		!hmac.Equal([]byte(signature(s.instanceKey, "launch/v1", stamp+"."+nonce)), []byte(mac)) {
		return false
	}
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	if s.usedLaunches == nil {
		s.usedLaunches = map[string]int64{}
	}
	for used, expiry := range s.usedLaunches {
		if expiry < now {
			delete(s.usedLaunches, used)
		}
	}
	if _, used := s.usedLaunches[nonce]; used || len(s.usedLaunches) >= 2048 {
		return false
	}
	s.usedLaunches[nonce] = until
	return true
}
