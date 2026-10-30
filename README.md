# github.com/pitchmentor/sdk/go

Official Go SDK for the [Pitch Mentor](https://pitchmentor.net) API (`https://api.pitchmentor.net/v1`).
Go 1.23+, standard library only, context-aware, `iter.Seq2` pagination.

API reference: https://pitchmentor.net/docs

## Install

```bash
go get github.com/pitchmentor/sdk/go@latest
```

## Authentication

Create a team API key (`pm_live_…`) in the Pitch Mentor dashboard. It is sent as `Authorization: Bearer pm_live_…`.
`NewClient("")` falls back to `PITCHMENTOR_API_KEY`, and the base URL to `PITCHMENTOR_BASE_URL`. Each key has scopes
(`calls:read`, `leads:write`, …); every method's doc comment names the scope it needs.

```go
import pitchmentor "github.com/pitchmentor/sdk/go"

client := pitchmentor.NewClient(os.Getenv("PITCHMENTOR_API_KEY"))
// options: pitchmentor.WithBaseURL(u), pitchmentor.WithHTTPClient(h), pitchmentor.WithMaxRetries(n)
```

## Quick start

```go
ctx := context.Background()

me, err := client.GetMe(ctx)
detail, err := client.GetCall(ctx, "call_id")
overview, err := client.GetAnalyticsOverview(ctx, &pitchmentor.GetAnalyticsOverviewParams{Period: pitchmentor.Ptr("30d")})
perMember, err := client.GetAnalyticsByMember(ctx, nil)
team, err := client.GetTeamConfig(ctx)
team, err = client.UpdateTeamConfig(ctx, &pitchmentor.UpdateTeamConfigBody{ToneGuidelines: pitchmentor.Ptr("Friendly, concise.")})
billing, err := client.GetBillingStatus(ctx)
usage, err := client.ListMemberUsage(ctx)
referrals, err := client.GetReferralStats(ctx)
csv, err := client.ExportCalls(ctx, &pitchmentor.ExportCallsParams{Format: pitchmentor.Ptr("csv")}) // []byte
```

Every API operation is a method named after its `operationId` (`ListCalls`, `GetCall`, `CreateWebhookEndpoint`, …).
Optional fields and params are pointers (`pitchmentor.Ptr(v)`); nil params are fine. Generated body structs omit nil
fields; to send an explicit `null` (clear a field), or to reach endpoints newer than the SDK, use `client.Do`:

```go
var lead pitchmentor.Lead
err := client.Do(ctx, "PATCH", "/v1/leads/"+id, nil, map[string]any{"phone": nil}, &lead)
```

## Pagination

List methods return one `*Page[T]` (`Data`, `HasMore`, `NextCursor`). Pass `NextCursor` as `Cursor` for the next page,
or range over the `…Iter` method, which walks every page:

```go
for call, err := range client.ListCallsIter(ctx, &pitchmentor.ListCallsParams{Period: pitchmentor.Ptr("7d")}) {
	if err != nil {
		return err
	}
	fmt.Println(call.ID, call.CallScore)
}
```

Paginated: `ListCalls`, `ListLeads`, `ListApiKeys`, `ListWebhookEndpoints`, `ListWebhookDeliveries`.

## Retries, timeouts and idempotency

429, 5xx and network errors are retried (`WithMaxRetries`, default 2) with exponential backoff, honoring `Retry-After`.
The default `http.Client` has a 60s timeout; cancel with `ctx`. Every POST carries an `Idempotency-Key`, generated unless
you pass one and reused across retries, so a retried POST never runs twice:

```go
lead, err := client.UpsertLead(ctx, &pitchmentor.UpsertLeadBody{Name: "Ada", ExternalID: pitchmentor.Ptr("crm-42")},
	pitchmentor.WithIdempotencyKey("import-42"))
```

## Errors

Non-2xx responses return `*pitchmentor.Error` with `Status`, `Code` (machine-readable, e.g. `not_found`,
`insufficient_scope`), `Message`, `Header`, `Body` and `RetryAfter()`.

```go
_, err := client.GetCall(ctx, "missing")
var apiErr *pitchmentor.Error
switch {
case pitchmentor.IsNotFound(err):
	// no such call
case pitchmentor.IsRateLimited(err):
	// back off
case errors.As(err, &apiErr):
	log.Println(apiErr.Status, apiErr.Code, apiErr.Message)
}
```

## Webhooks

Webhooks are signed with [Standard Webhooks](https://www.standardwebhooks.com/) (`webhook-id`, `webhook-timestamp`,
`webhook-signature`). `VerifyWebhook` checks the signature against your endpoint's `whsec_…` secret, rejects timestamps
older than `WebhookTolerance` (5 minutes), and returns the event; decode `Data` based on `Type`. Failures wrap
`ErrWebhookVerification`. Always pass the raw request body.

```go
http.HandleFunc("/hooks/pitchmentor", func(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	event, err := pitchmentor.VerifyWebhook(os.Getenv("PITCHMENTOR_WEBHOOK_SECRET"), r.Header, body)
	if err != nil {
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}
	if event.Type == "call.completed" {
		var call pitchmentor.CallRecord
		_ = json.Unmarshal(event.Data, &call)
	}
	w.WriteHeader(http.StatusNoContent)
})
```

## License

MIT
