package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

const (
	testAccount = "11111111111111111111111111111111"
	testTokenID = "22222222222222222222222222222222"
	testGroup   = "33333333333333333333333333333333"
	testPolicy  = "44444444-4444-4444-4444-444444444444"
	issuedToken = "issued-token-test-canary"
	authToken   = "authentication-token-test-canary"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func reply(req *http.Request, code int, result string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(result)),
		Request:    req,
	}
}

func resultJSON(result string) string {
	return `{"success":true,"errors":[],"messages":[],"result":` + result + "}"
}

func spendResult(version, threshold int) string {
	return resultJSON(fmt.Sprintf(`{"id":%q,"name":"test cap","product_id":"workers","action":"usage_block","enabled":true,"threshold_cents":%d,"notification_email":"","version":%d}`, testPolicy, threshold, version))
}

func checkRequest(t *testing.T, req *http.Request, method, path string) {
	t.Helper()
	if req.Method != method || req.URL.Path != path || req.URL.Host != "api.cloudflare.com" || req.Header.Get("Authorization") != "Bearer "+authToken {
		t.Fatalf("unexpected request method, endpoint, or authentication")
	}
	if req.Header.Get("X-Auth-Key") != "" || req.Header.Get("X-Injected") != "" {
		t.Fatal("inherited an unrelated credential or header")
	}
}

func TestSpendPolicyLifecycle(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_KEY", "unused")
	t.Setenv("CLOUDFLARE_BASE_URL", "https://unexpected.example")
	t.Setenv("CLOUDFLARE_CUSTOM_HEADERS", "X-Injected: unrelated")
	ctx := context.Background()
	base := "/client/v4/accounts/" + testAccount + "/billing/spend-policies"
	step := 0
	c := newClient(authToken, apiEndpoint, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		step++
		switch step {
		case 1:
			checkRequest(t, req, http.MethodPost, base)
			return reply(req, 200, spendResult(1, 2500)), nil
		case 2:
			checkRequest(t, req, http.MethodGet, base+"/"+testPolicy)
			return reply(req, 200, spendResult(2, 2500)), nil
		case 3:
			checkRequest(t, req, http.MethodPut, base+"/"+testPolicy)
			if req.Header.Get("If-Match") != "2" {
				t.Fatal("update dropped the refreshed version")
			}
			return reply(req, 412, `{"success":false,"errors":[{"message":"`+issuedToken+`"}]}`), nil
		case 4:
			checkRequest(t, req, http.MethodPut, base+"/"+testPolicy)
			if req.Header.Get("If-Match") != "2" {
				t.Fatal("update dropped the refreshed version")
			}
			return reply(req, 200, spendResult(3, 3500)), nil
		case 5:
			checkRequest(t, req, http.MethodDelete, base+"/"+testPolicy)
			if req.Header.Get("If-Match") != "3" {
				t.Fatal("delete dropped the updated version")
			}
			return reply(req, 404, `{"success":false,"errors":[]}`), nil
		default:
			t.Fatal("unexpected request or retry")
			return nil, errors.New("unexpected request")
		}
	}))

	r := &spendPolicyResource{api: c.spend}
	var sch resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sch)
	m := spendPolicyModel{AccountID: types.StringValue(testAccount), Name: types.StringValue("test cap"), ProductID: types.StringValue("workers"),
		Action: types.StringValue("usage_block"), Enabled: types.BoolValue(true), ThresholdCents: types.Int64Value(2500)}
	create := resource.CreateResponse{State: tfsdk.State{Schema: sch.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: makePlan(t, sch.Schema, &m)}, &create)
	if create.Diagnostics.HasError() {
		t.Fatal(create.Diagnostics)
	}

	read := resource.ReadResponse{State: create.State}
	r.Read(ctx, resource.ReadRequest{State: create.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	read.State.Get(ctx, &m)
	if m.Version.ValueInt64() != 2 {
		t.Fatal("missing refreshed version")
	}
	m.ThresholdCents = types.Int64Value(3500)
	updateReq := resource.UpdateRequest{State: read.State, Plan: makePlan(t, sch.Schema, &m)}
	conflict := resource.UpdateResponse{State: read.State}
	r.Update(ctx, updateReq, &conflict)
	if !conflict.Diagnostics.HasError() || strings.Contains(fmt.Sprint(conflict.Diagnostics), issuedToken) || step != 3 {
		t.Fatal("unsafe conflict handling")
	}
	update := resource.UpdateResponse{State: read.State}
	r.Update(ctx, updateReq, &update)
	if update.Diagnostics.HasError() {
		t.Fatal(update.Diagnostics)
	}
	var deleteResp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: update.State}, &deleteResp)
	if deleteResp.Diagnostics.HasError() || step != 5 {
		t.Fatal("delete failed or retried")
	}
}

func makePlan(t *testing.T, sch schema.Schema, model any) tfsdk.Plan {
	t.Helper()
	plan := tfsdk.Plan{Schema: sch}
	if d := plan.Set(context.Background(), model); d.HasError() {
		t.Fatal(d)
	}
	return plan
}

func tokenResult(includeSecret bool) string {
	secret := ""
	if includeSecret {
		secret = fmt.Sprintf(`,"value":%q`, issuedToken)
	}
	return resultJSON(fmt.Sprintf(`{"id":%q,"name":"test token","status":"active","expires_on":"2030-01-01T00:00:00Z","policies":[{"id":"remote-policy","effect":"allow","permission_groups":[{"id":%q}],"resources":{"com.cloudflare.api.account.%s":"*"}}]%s}`, testTokenID, testGroup, testAccount, secret))
}

func movedToken(t *testing.T) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	data, err := json.Marshal(map[string]any{
		"account_id": testAccount, "id": testTokenID, "name": "test token", "expires_on": "2030-01-01T09:00:00+09:00",
		"status": "active", "value": issuedToken, "condition": nil,
		"policies": []any{map[string]any{"effect": "allow", "permission_groups": []any{map[string]string{"id": testGroup}},
			"resources": fmt.Sprintf(`{ "com.cloudflare.api.account.%s": "*" }`, testAccount)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := resource.MoveStateResponse{TargetState: tfsdk.State{Schema: accountTokenSchema()}}
	moveCloudflareAccountToken(ctx, resource.MoveStateRequest{
		SourceTypeName: "cloudflare_account_token", SourceSchemaVersion: 501, SourceProviderAddress: "registry.terraform.io/cloudflare/cloudflare",
		SourceRawState: &tfprotov6.RawState{JSON: data},
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	return resp.TargetState
}

func TestTokenMoveReadAndUpdateKeepIdentityAndValue(t *testing.T) {
	ctx := context.Background()
	state := movedToken(t)
	var before accountTokenModel
	if d := state.Get(ctx, &before); d.HasError() {
		t.Fatal(d)
	}
	step := 0
	c := newClient(authToken, apiEndpoint, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		step++
		path := "/client/v4/accounts/" + testAccount + "/tokens/" + testTokenID
		if step == 1 {
			checkRequest(t, req, http.MethodGet, path)
		} else {
			checkRequest(t, req, http.MethodPut, path)
		}
		if step == 2 {
			body, err := io.ReadAll(req.Body)
			if err != nil || strings.Contains(string(body), issuedToken) || !strings.Contains(string(body), testGroup) {
				t.Fatal("update sent the issued token or dropped permissions")
			}
		}
		return reply(req, 200, tokenResult(false)), nil
	}))
	r := &accountTokenResource{api: c.token}
	read := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &read)
	if read.Diagnostics.HasError() || !state.Raw.Equal(read.State.Raw) {
		t.Fatal("refresh caused drift or dropped migrated state")
	}

	plan := before
	plan.Name = types.StringValue("updated token")
	update := resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{State: state, Plan: makePlan(t, accountTokenSchema(), &plan)}, &update)
	var after accountTokenModel
	if d := update.State.Get(ctx, &after); d.HasError() || update.Diagnostics.HasError() {
		t.Fatal("update failed")
	}
	if !before.Value.Equal(after.Value) || !before.ID.Equal(after.ID) || step != 2 {
		t.Fatal("token ID or value changed")
	}
}

func TestTokenCreateDoesNotRetryOrLeakOnAmbiguousFailure(t *testing.T) {
	ctx := context.Background()
	state := movedToken(t)
	var model accountTokenModel
	state.Get(ctx, &model)
	calls := 0
	c := newClient(authToken, apiEndpoint, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return reply(req, 500, `{"success":false,"errors":[{"message":"`+authToken+" "+issuedToken+`"}]}`), nil
	}))
	r := &accountTokenResource{api: c.token}
	resp := resource.CreateResponse{State: state}
	r.Create(ctx, resource.CreateRequest{Plan: makePlan(t, accountTokenSchema(), &model)}, &resp)
	diagnostics := fmt.Sprint(resp.Diagnostics)
	if !resp.Diagnostics.HasError() || calls != 1 || strings.Contains(diagnostics, authToken) || strings.Contains(diagnostics, issuedToken) {
		t.Fatal("creation was retried or exposed sensitive information")
	}
}

func TestClientDoesNotFollowRedirect(t *testing.T) {
	ctx := context.Background()
	calls := 0
	c := newClient(authToken, apiEndpoint, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		res := reply(req, 307, tokenResult(false))
		res.Header.Set("Location", "https://unexpected.example/credentials")
		return res, nil
	}))
	r := &accountTokenResource{api: c.token}
	state := movedToken(t)
	read := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &read)
	if calls != 1 || !read.Diagnostics.HasError() {
		t.Fatal("redirect was followed or accepted")
	}
}

func TestTokenNoOpUpdateDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	state := movedToken(t)
	var model accountTokenModel
	if d := state.Get(ctx, &model); d.HasError() {
		t.Fatal(d)
	}
	c := newClient(authToken, apiEndpoint, roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("a state-only move attempted a remote mutation")
		return nil, errors.New("unexpected request")
	}))
	r := &accountTokenResource{api: c.token}
	resp := resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{State: state, Plan: makePlan(t, accountTokenSchema(), &model)}, &resp)
	if resp.Diagnostics.HasError() || !resp.State.Raw.Equal(state.Raw) {
		t.Fatal("a state-only move failed to keep the token state")
	}
}
