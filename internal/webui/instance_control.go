package webui

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// The local registry key identifies this daemon incarnation. Seal control
// requests so a stale listener at a recycled port cannot learn checkout paths
// or browser tickets. Each response is bound to its originating request.
type daemonRequest struct {
	Commander   commanderBinding `json:"commander,omitzero"`
	Action      string           `json:"action"`
	Dir         string           `json:"dir,omitempty"`
	Destination Destination      `json:"destination,omitempty"`
	Mode        string           `json:"mode,omitempty"`
	Issued      int64            `json:"issued"`
}

type daemonReply struct {
	ScopedCommander bool   `json:"scoped_commander,omitempty"`
	URL             string `json:"url,omitempty"`
	Error           string `json:"error,omitempty"`
}

func (i instance) controlCipher() (cipher.AEAD, error) {
	key, err := base64.RawURLEncoding.DecodeString(i.Key)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (i instance) control(ctx context.Context, op daemonRequest) (daemonReply, error) {
	// Older daemons inherited ambient Commander credentials. Verify support before
	// disclosing a binding or accepting a browser URL from a running daemon.
	if op.Action == "register" {
		peer, err := i.control(ctx, daemonRequest{Action: "status"})
		if err != nil {
			return daemonReply{}, err
		}
		if !peer.ScopedCommander {
			return daemonReply{}, errors.New("running devflow UI daemon predates scoped Agent Commander connections; run devflow ui --stop and reopen it")
		}
		op.Action = "register_scoped"
	}
	aead, err := i.controlCipher()
	if err != nil {
		return daemonReply{}, err
	}
	op.Issued = time.Now().Unix()
	plain, err := json.Marshal(op)
	if err != nil {
		return daemonReply{}, err
	}
	//nolint:gosec // NewGCMWithRandomNonce generates and prepends the nonce; its nonce argument must be nil.
	data := aead.Seal(nil, nil, plain, []byte("devflow-control/v1"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.origin()+"/_devflow/control", bytes.NewReader(data))
	if err != nil {
		return daemonReply{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	// Reusing a visible tab can wait for a browser acknowledgement.
	client := *instanceHTTP
	client.Timeout = 12 * time.Second
	//nolint:gosec // instance.origin always constructs a literal 127.0.0.1 address and redirects are disabled.
	resp, err := client.Do(req)
	if err != nil {
		return daemonReply{}, fmt.Errorf("contact devflow daemon: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return daemonReply{}, err
	}
	reqHash := sha256.Sum256(data)
	res, err := aead.Open(nil, nil, body, reqHash[:])
	if err != nil || resp.StatusCode != http.StatusOK {
		return daemonReply{}, errors.New("could not verify devflow daemon response")
	}
	var reply daemonReply
	if err := json.Unmarshal(res, &reply); err != nil {
		return daemonReply{}, err
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}

func (d *daemon) handleControl(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" || r.Header.Get("Content-Type") != "application/octet-stream" {
		http.Error(w, "local daemon request required", http.StatusForbidden)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil {
		http.Error(w, "invalid control request", http.StatusBadRequest)
		return
	}
	aead, err := d.controlCipher()
	if err != nil {
		http.Error(w, "daemon key unavailable", http.StatusInternalServerError)
		return
	}
	plain, err := aead.Open(nil, nil, data, []byte("devflow-control/v1"))
	if err != nil {
		http.Error(w, "invalid control request", http.StatusForbidden)
		return
	}
	var op daemonRequest
	now := time.Now().Unix()
	if json.Unmarshal(plain, &op) != nil || op.Issued < now-30 || op.Issued > now+5 {
		http.Error(w, "stale control request", http.StatusForbidden)
		return
	}
	hash := sha256.Sum256(data)
	d.controlMu.Lock()
	for k, v := range d.usedControl {
		if v < now {
			delete(d.usedControl, k)
		}
	}
	_, seen := d.usedControl[string(hash[:])]
	if seen || len(d.usedControl) >= 2048 {
		d.controlMu.Unlock()
		http.Error(w, "repeated control request", http.StatusForbidden)
		return
	}
	d.usedControl[string(hash[:])] = op.Issued + 31
	d.controlMu.Unlock()
	result := daemonReply{ScopedCommander: true}
	switch op.Action {
	case "status", "stop":
	case "register":
		// A legacy launcher supplies no trusted identity.
		op.Commander = commanderBinding{}
		fallthrough
	case "register_scoped":
		result.URL, err = d.register(r.Context(), op)
		if err != nil {
			result.Error = err.Error()
		}
	default:
		result.Error = "unknown console daemon operation"
	}
	payload, err := json.Marshal(result)
	if err != nil {
		http.Error(w, "invalid control response", http.StatusInternalServerError)
		return
	}
	//nolint:gosec // NewGCMWithRandomNonce generates the response nonce internally.
	encoded := aead.Seal(nil, nil, payload, hash[:])
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(encoded)))
	//nolint:gosec // The response is authenticated binary ciphertext, served as nosniff application/octet-stream.
	if _, err = w.Write(encoded); err == nil && op.Action == "stop" {
		_ = http.NewResponseController(w).Flush()
		d.cancel()
	}
}
