package provider

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func (*accountTokenResource) MoveState(context.Context) []resource.StateMover {
	return []resource.StateMover{{StateMover: moveCloudflareAccountToken}}
}

func moveCloudflareAccountToken(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
	if req.SourceTypeName != "cloudflare_account_token" || req.SourceSchemaVersion != 501 {
		return
	}
	parts := strings.Split(req.SourceProviderAddress, "/")
	if len(parts) < 2 || len(parts) > 3 || strings.Join(parts[len(parts)-2:], "/") != "cloudflare/cloudflare" {
		return
	}
	var source struct {
		AccountID string  `json:"account_id"`
		ID        string  `json:"id"`
		Name      string  `json:"name"`
		ExpiresOn string  `json:"expires_on"`
		NotBefore string  `json:"not_before"`
		Status    string  `json:"status"`
		Value     *string `json:"value"`
		Policies  []struct {
			Effect           string               `json:"effect"`
			PermissionGroups []apiTokenPermission `json:"permission_groups"`
			Resources        string               `json:"resources"`
		} `json:"policies"`
		Condition *apiTokenCondition `json:"condition"`
	}
	if req.SourceRawState == nil || json.Unmarshal(req.SourceRawState.JSON, &source) != nil ||
		!accountIDPattern.MatchString(source.AccountID) || !accountIDPattern.MatchString(source.ID) || len(source.Policies) == 0 {
		resp.Diagnostics.AddError("Cannot move account token state", "The Cloudflare source state is incomplete or has an unsupported format.")
		return
	}
	record := tokenRecord{tokenFields: tokenFields{Name: source.Name, Condition: source.Condition}, ID: source.ID, Status: source.Status}
	for _, policy := range source.Policies {
		var scopes map[string]json.RawMessage
		if err := json.Unmarshal([]byte(policy.Resources), &scopes); err != nil || len(scopes) == 0 {
			resp.Diagnostics.AddError("Cannot move account token policy", "A source policy has invalid resource selectors.")
			return
		}
		record.Policies = append(record.Policies, apiTokenPolicy{
			Effect: policy.Effect, PermissionGroups: policy.PermissionGroups, Resources: json.RawMessage(policy.Resources),
		})
	}
	m := accountTokenModel{
		AccountID: types.StringValue(source.AccountID), ID: types.StringValue(source.ID),
		Value: types.StringPointerValue(source.Value),
	}
	resp.Diagnostics.Append(m.setFields(ctx, record)...)
	if source.ExpiresOn != "" {
		m.ExpiresOn = types.StringValue(source.ExpiresOn)
	}
	if source.NotBefore != "" {
		m.NotBefore = types.StringValue(source.NotBefore)
	}
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.TargetState.Set(ctx, &m)...)
	}
}
