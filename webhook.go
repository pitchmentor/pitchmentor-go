package pitchmentor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrWebhookVerification is returned (wrapped) when a webhook fails verification.
var ErrWebhookVerification = errors.New("pitchmentor: webhook verification failed")

// WebhookEvent is a verified webhook payload. Decode Data based on Type
// (e.g. "call.completed" → CallRecord, "lead.created" → Lead).
type WebhookEvent struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// WebhookTolerance is the max age of webhook-timestamp accepted by VerifyWebhook.
var WebhookTolerance = 5 * time.Minute

// VerifyWebhook verifies a Pitch Mentor webhook (Standard Webhooks) and returns the parsed event.
// Pass the raw request body exactly as received.
func VerifyWebhook(secret string, header http.Header, body []byte) (*WebhookEvent, error) {
	return verifyWebhookAt(secret, header, body, time.Now())
}

func verifyWebhookAt(secret string, header http.Header, body []byte, now time.Time) (*WebhookEvent, error) {
	fail := func(msg string) error { return errors.Join(ErrWebhookVerification, errors.New(msg)) }
	id, ts, sigs := header.Get("webhook-id"), header.Get("webhook-timestamp"), header.Get("webhook-signature")
	if id == "" || ts == "" || sigs == "" {
		return nil, fail("missing webhook-id, webhook-timestamp or webhook-signature header")
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || now.Sub(time.Unix(sec, 0)).Abs() > WebhookTolerance {
		return nil, fail("timestamp is invalid or outside the tolerance window")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return nil, fail("invalid secret")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	expected := []byte(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	for _, part := range strings.Split(sigs, " ") {
		version, sig, _ := strings.Cut(part, ",")
		if version == "v1" && hmac.Equal([]byte(sig), expected) {
			var event WebhookEvent
			if err := json.Unmarshal(body, &event); err != nil {
				return nil, fail("body is not valid JSON")
			}
			return &event, nil
		}
	}
	return nil, fail("no matching signature")
}
