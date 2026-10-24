package preview

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/0226zy/protoforge/internal/kit/secret"
)

type ticketPayload struct {
	ConvID string `json:"conv_id"`
	Host   string `json:"host"`
	Nonce  string `json:"nonce"`
	Exp    int64  `json:"exp"`
}
type Tickets struct {
	master []byte
	ttl    time.Duration
	mu     sync.Mutex
	nonces map[string]int64
}

func NewTickets(master []byte, ttl time.Duration) *Tickets {
	return &Tickets{master: master, ttl: ttl, nonces: map[string]int64{}}
}
func (t *Tickets) Issue(convID, host string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	p := ticketPayload{ConvID: convID, Host: host, Nonce: hex.EncodeToString(b), Exp: time.Now().Add(t.ttl).Unix()}
	raw, _ := json.Marshal(p)
	body := base64.RawURLEncoding.EncodeToString(raw)
	t.mu.Lock()
	t.nonces[p.Nonce] = p.Exp
	for n, exp := range t.nonces {
		if exp < time.Now().Unix() {
			delete(t.nonces, n)
		}
	}
	t.mu.Unlock()
	return body + "." + secret.Sign(t.master, "preview-ticket", body), nil
}
func (t *Tickets) Consume(ticket, host string) (string, error) {
	parts := strings.Split(ticket, ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid ticket")
	}
	expected := secret.Sign(t.master, "preview-ticket", parts[0])
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return "", fmt.Errorf("invalid ticket")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", err
	}
	var p ticketPayload
	if json.Unmarshal(raw, &p) != nil || p.Host != host || p.Exp < time.Now().Unix() {
		return "", fmt.Errorf("invalid ticket")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.nonces[p.Nonce]; !ok {
		return "", fmt.Errorf("ticket already used")
	}
	delete(t.nonces, p.Nonce)
	return p.ConvID, nil
}

type cookiePayload struct {
	ConvID string `json:"conv_id"`
	Host   string `json:"host"`
	Exp    int64  `json:"exp"`
}

func signCookie(master []byte, convID, host string, ttl time.Duration) string {
	raw, _ := json.Marshal(cookiePayload{ConvID: convID, Host: host, Exp: time.Now().Add(ttl).Unix()})
	body := base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + secret.Sign(master, "preview-cookie", body)
}
func verifyCookie(master []byte, value, host string) (string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid cookie")
	}
	expected := secret.Sign(master, "preview-cookie", parts[0])
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return "", fmt.Errorf("invalid cookie")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", err
	}
	var p cookiePayload
	if json.Unmarshal(raw, &p) != nil || p.Host != host || p.Exp < time.Now().Unix() {
		return "", fmt.Errorf("invalid cookie")
	}
	return p.ConvID, nil
}
