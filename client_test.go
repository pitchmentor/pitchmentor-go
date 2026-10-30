package pitchmentor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recorded struct {
	method, path, rawQuery, body string
	header                       http.Header
}

// server replays queued responses (status, body, headers) and records requests.
func server(t *testing.T, responses ...func(w http.ResponseWriter)) (*Client, *[]recorded) {
	t.Helper()
	var reqs []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqs = append(reqs, recorded{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, string(b), r.Header.Clone()})
		responses[len(reqs)-1](w)
	}))
	t.Cleanup(srv.Close)
	c := NewClient("pm_live_test", WithBaseURL(srv.URL+"/"))
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c, &reqs
}

func reply(status int, body string, kv ...string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		for i := 0; i < len(kv); i += 2 {
			w.Header().Set(kv[i], kv[i+1])
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

const emptyPage = `{"data":[],"has_more":false,"next_cursor":null}`

func TestRequestBuilding(t *testing.T) {
	c, reqs := server(t, reply(200, emptyPage), reply(201, `{"lead":{"id":"l1"},"action":"created"}`), reply(200, `{"id":"c1","archivedAt":null}`))
	ctx := context.Background()
	if _, err := c.ListWebhookDeliveries(ctx, "we/1", &ListWebhookDeliveriesParams{Status: Ptr("failed"), Limit: Ptr(5)}); err != nil {
		t.Fatal(err)
	}
	r := (*reqs)[0]
	if r.method != "GET" || r.path != "/v1/webhooks/we%2F1/deliveries" || r.rawQuery != "limit=5&status=failed" {
		t.Fatalf("bad request %+v", r)
	}
	if r.header.Get("Authorization") != "Bearer pm_live_test" || r.header.Get("Idempotency-Key") != "" {
		t.Fatalf("bad headers %v", r.header)
	}

	res, err := c.UpsertLead(ctx, &UpsertLeadBody{Name: "Ada", Email: Ptr("ada@example.com")})
	if err != nil || res.Lead.ID != "l1" {
		t.Fatal(res, err)
	}
	var sent map[string]any
	json.Unmarshal([]byte((*reqs)[1].body), &sent)
	if sent["name"] != "Ada" || sent["email"] != "ada@example.com" || len(sent) != 2 {
		t.Fatalf("body %s", (*reqs)[1].body)
	}
	if len((*reqs)[1].header.Get("Idempotency-Key")) != 36 || (*reqs)[1].header.Get("Content-Type") != "application/json" {
		t.Fatal("missing idempotency key or content type")
	}

	if _, err := c.ArchiveCall(ctx, "c1", WithIdempotencyKey("mine")); err != nil {
		t.Fatal(err)
	}
	if h := (*reqs)[2].header; h.Get("Idempotency-Key") != "mine" || h.Get("Content-Type") != "" {
		t.Fatalf("bodyless POST headers %v", h)
	}
}

func TestPagination(t *testing.T) {
	c, reqs := server(t,
		reply(200, `{"data":[{"id":"a"},{"id":"b"}],"has_more":true,"next_cursor":"b"}`),
		reply(200, `{"data":[{"id":"c"}],"has_more":false,"next_cursor":null}`))
	var ids []string
	for call, err := range c.ListCallsIter(context.Background(), &ListCallsParams{Limit: Ptr(2), Archived: Ptr(true)}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, call.ID)
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Fatal(ids)
	}
	if q := (*reqs)[1].rawQuery; !strings.Contains(q, "cursor=b") || !strings.Contains(q, "archived=true") {
		t.Fatal(q)
	}
}

func TestRetries(t *testing.T) {
	c, reqs := server(t,
		reply(429, `{"error":{"code":"rate_limited","message":"slow"}}`, "Retry-After", "3"),
		reply(503, `oops`),
		reply(200, `{"id":"c1","archivedAt":"2026-01-01T00:00:00.000Z"}`))
	var slept []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	res, err := c.ArchiveCall(context.Background(), "c1")
	if err != nil || res.ArchivedAt == nil || res.ArchivedAt.Year() != 2026 {
		t.Fatal(res, err)
	}
	if len(*reqs) != 3 || slept[0] != 3*time.Second {
		t.Fatal(len(*reqs), slept)
	}
	if (*reqs)[0].header.Get("Idempotency-Key") != (*reqs)[2].header.Get("Idempotency-Key") {
		t.Fatal("idempotency key changed between retries")
	}
}

func TestErrors(t *testing.T) {
	c, reqs := server(t,
		reply(404, `{"error":{"code":"not_found","message":"Call not found"}}`),
		reply(429, `{"error":{"code":"rate_limited","message":"slow"}}`, "Retry-After", "7"))
	_, err := c.GetCall(context.Background(), "nope")
	var e *Error
	if !errors.As(err, &e) || e.Code != "not_found" || e.Message != "Call not found" || !IsNotFound(err) || len(*reqs) != 1 {
		t.Fatal(err)
	}
	c.MaxRetries = 0
	_, err = c.GetMe(context.Background())
	if !IsRateLimited(err) || err.(*Error).RetryAfter() != 7*time.Second {
		t.Fatal(err)
	}
}

func TestExportCSV(t *testing.T) {
	c, _ := server(t, func(w http.ResponseWriter) { w.Header().Set("Content-Type", "text/csv"); io.WriteString(w, "id\nc1\n") })
	out, err := c.ExportCalls(context.Background(), nil)
	if err != nil || string(out) != "id\nc1\n" {
		t.Fatal(string(out), err)
	}
}

// Known-good vector from the backend's test (= Standard Webhooks reference vector)
const (
	vSecret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	vID     = "msg_p5jXN8AQM9LWM0D4loKWxJek"
	vTS     = 1614265330
	vBody   = `{"test": 2432232314}`
	vSig    = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
)

func vHeader(kv ...string) http.Header {
	h := http.Header{}
	h.Set("webhook-id", vID)
	h.Set("webhook-timestamp", "1614265330")
	h.Set("webhook-signature", vSig)
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestVerifyWebhook(t *testing.T) {
	at := time.Unix(vTS, 0)
	if _, err := verifyWebhookAt(vSecret, vHeader(), []byte(vBody), at); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyWebhookAt(vSecret, vHeader("webhook-signature", "v1,bogus "+vSig), []byte(vBody), at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		secret string
		h      http.Header
		body   string
		now    time.Time
	}{
		{vSecret, vHeader(), vBody + " ", at},
		{"whsec_YW5vdGhlciBzZWNyZXQga2V5", vHeader(), vBody, at},
		{vSecret, vHeader("webhook-id", "msg_2"), vBody, at},
		{vSecret, vHeader(), vBody, at.Add(301 * time.Second)},
		{vSecret, vHeader("webhook-signature", ""), vBody, at},
	}
	for i, tc := range cases {
		if _, err := verifyWebhookAt(tc.secret, tc.h, []byte(tc.body), tc.now); !errors.Is(err, ErrWebhookVerification) {
			t.Errorf("case %d: expected verification error, got %v", i, err)
		}
	}
}
