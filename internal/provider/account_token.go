package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	cloudflare "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/accounts"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/shared"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

type accountTokenResource struct {
	api *accounts.TokenService
}

type accountTokenModel struct {
	AccountID types.String `tfsdk:"account_id"`
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Policies  types.List   `tfsdk:"policies"`
	ExpiresOn types.String `tfsdk:"expires_on"`
	NotBefore types.String `tfsdk:"not_before"`
	Condition types.Object `tfsdk:"condition"`
	Status    types.String `tfsdk:"status"`
	Value     types.String `tfsdk:"value"`
}

type tokenPolicyModel struct {
	Effect           types.String `tfsdk:"effect"`
	PermissionGroups types.List   `tfsdk:"permission_groups"`
	Resources        types.String `tfsdk:"resources"`
}

type tokenPermissionModel struct {
	ID types.String `tfsdk:"id"`
}

type tokenConditionModel struct {
	RequestIP types.Object `tfsdk:"request_ip"`
}

type tokenIPModel struct {
	In    types.List `tfsdk:"in"`
	NotIn types.List `tfsdk:"not_in"`
}

type tokenFields struct {
	Name      string             `json:"name"`
	ExpiresOn *time.Time         `json:"expires_on,omitempty"`
	NotBefore *time.Time         `json:"not_before,omitempty"`
	Policies  []apiTokenPolicy   `json:"policies"`
	Condition *apiTokenCondition `json:"condition,omitempty"`
}

type apiTokenPolicy struct {
	Effect           string               `json:"effect"`
	PermissionGroups []apiTokenPermission `json:"permission_groups"`
	Resources        json.RawMessage      `json:"resources"`
}

type apiTokenPermission struct {
	ID string `json:"id"`
}

type apiTokenCondition struct {
	RequestIP *apiTokenIP `json:"request_ip,omitempty"`
}

type apiTokenIP struct {
	In    []string `json:"in,omitempty"`
	NotIn []string `json:"not_in,omitempty"`
}

type tokenRecord struct {
	tokenFields
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Value  *string `json:"value"`
}

var (
	_ resource.ResourceWithConfigure      = (*accountTokenResource)(nil)
	_ resource.ResourceWithImportState    = (*accountTokenResource)(nil)
	_ resource.ResourceWithValidateConfig = (*accountTokenResource)(nil)
	_ resource.ResourceWithMoveState      = (*accountTokenResource)(nil)
)

func newAccountTokenResource() resource.Resource { return &accountTokenResource{} }

func (*accountTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_account_token"
}

func accountTokenSchema() schema.Schema {
	return schema.Schema{
		Description: "Issue and manage a Cloudflare account-owned API token. The token value is stored in Terraform state when created.",
		Attributes: map[string]schema.Attribute{
			"account_id": schema.StringAttribute{Description: "Cloudflare account ID. Changing it replaces the token.", Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"id":         schema.StringAttribute{Description: "Account API token ID.", Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":       schema.StringAttribute{Description: "Token name, up to 120 bytes.", Required: true},
			"expires_on": schema.StringAttribute{Description: "Optional RFC3339 timestamp at which the token expires.", Optional: true},
			"not_before": schema.StringAttribute{Description: "Optional RFC3339 timestamp before which the token cannot be used.", Optional: true},
			"status":     schema.StringAttribute{Description: "Cloudflare's reported token status.", Computed: true},
			"value":      schema.StringAttribute{Description: "Bearer token returned at creation. Terraform stores this value in state. Import yields no token value.", Sensitive: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"policies": schema.ListNestedAttribute{
				Description: "Explicit allow or deny policies for this token.", Required: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"effect":    schema.StringAttribute{Description: "allow or deny.", Required: true},
					"resources": schema.StringAttribute{Description: "JSON object mapping Cloudflare resource selectors to their scopes. Use jsonencode to construct it.", Required: true},
					"permission_groups": schema.ListNestedAttribute{
						Description: "Permission groups granted or denied by this policy.", Required: true,
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"id": schema.StringAttribute{Description: "Cloudflare permission group ID.", Required: true},
						}},
					},
				}},
			},
			"condition": schema.SingleNestedAttribute{
				Description: "Restrict requests using the token.", Optional: true,
				Attributes: map[string]schema.Attribute{
					"request_ip": schema.SingleNestedAttribute{
						Description: "IP allow and deny lists for requests using this token.", Required: true,
						Attributes: map[string]schema.Attribute{
							"in":     schema.ListAttribute{Description: "Allowed IPv4 or IPv6 CIDRs.", ElementType: types.StringType, Optional: true},
							"not_in": schema.ListAttribute{Description: "Denied IPv4 or IPv6 CIDRs.", ElementType: types.StringType, Optional: true},
						},
					},
				},
			},
		},
	}
}

func (*accountTokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = accountTokenSchema()
}

func (r *accountTokenResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider configuration", "The Cloudflare client was not configured.")
		return
	}
	r.api = c.token
}

func (*accountTokenResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m accountTokenModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if !resp.Diagnostics.HasError() {
		_, diags := m.requestFields(ctx)
		resp.Diagnostics.Append(diags...)
	}
}

func (m accountTokenModel) requestFields(ctx context.Context) (tokenFields, diag.Diagnostics) {
	var diags diag.Diagnostics
	fields := tokenFields{Name: m.Name.ValueString()}
	if known(m.AccountID) && !accountIDPattern.MatchString(m.AccountID.ValueString()) {
		diags.AddAttributeError(path.Root("account_id"), "Invalid account ID", "Use the 32-character lowercase hexadecimal Cloudflare account ID.")
	}
	if known(m.Name) && (strings.TrimSpace(fields.Name) == "" || len(fields.Name) > 120) {
		diags.AddAttributeError(path.Root("name"), "Invalid token name", "Use a nonempty name of at most 120 bytes.")
	}
	fields.ExpiresOn = parseTime(m.ExpiresOn, "expires_on", &diags)
	fields.NotBefore = parseTime(m.NotBefore, "not_before", &diags)
	if fields.ExpiresOn != nil && fields.NotBefore != nil && !fields.ExpiresOn.After(*fields.NotBefore) {
		diags.AddAttributeError(path.Root("expires_on"), "Invalid token lifetime", "The expiration must be later than not_before.")
	}
	if !m.Policies.IsNull() && !m.Policies.IsUnknown() {
		var policies []types.Object
		diags.Append(m.Policies.ElementsAs(ctx, &policies, false)...)
		if len(policies) == 0 {
			diags.AddAttributeError(path.Root("policies"), "Missing token policies", "Specify at least one access policy.")
		}
		for i, item := range policies {
			if item.IsUnknown() || item.IsNull() {
				continue
			}
			var policy tokenPolicyModel
			diags.Append(item.As(ctx, &policy, basetypes.ObjectAsOptions{})...)
			policyPath := path.Root("policies").AtListIndex(i)
			wire := apiTokenPolicy{Effect: policy.Effect.ValueString(), Resources: json.RawMessage(policy.Resources.ValueString())}
			if known(policy.Effect) && wire.Effect != "allow" && wire.Effect != "deny" {
				diags.AddAttributeError(policyPath.AtName("effect"), "Invalid policy effect", "Use allow or deny.")
			}
			if known(policy.Resources) {
				var scopes map[string]json.RawMessage
				if err := json.Unmarshal(wire.Resources, &scopes); err != nil || len(scopes) == 0 {
					diags.AddAttributeError(policyPath.AtName("resources"), "Invalid policy resources", "Use a nonempty JSON object of Cloudflare resource selectors.")
				}
			}
			if !policy.PermissionGroups.IsUnknown() && !policy.PermissionGroups.IsNull() {
				var groups []types.Object
				diags.Append(policy.PermissionGroups.ElementsAs(ctx, &groups, false)...)
				if len(groups) == 0 {
					diags.AddAttributeError(policyPath.AtName("permission_groups"), "Missing permission groups", "Specify at least one Cloudflare permission group ID.")
				}
				for j, entry := range groups {
					if entry.IsUnknown() || entry.IsNull() {
						continue
					}
					var group tokenPermissionModel
					diags.Append(entry.As(ctx, &group, basetypes.ObjectAsOptions{})...)
					if known(group.ID) && !accountIDPattern.MatchString(group.ID.ValueString()) {
						diags.AddAttributeError(policyPath.AtName("permission_groups").AtListIndex(j).AtName("id"), "Invalid permission group ID", "Use the 32-character hexadecimal group ID from Cloudflare.")
					}
					wire.PermissionGroups = append(wire.PermissionGroups, apiTokenPermission{ID: group.ID.ValueString()})
				}
			}
			fields.Policies = append(fields.Policies, wire)
		}
	}
	if !m.Condition.IsNull() && !m.Condition.IsUnknown() {
		var condition tokenConditionModel
		diags.Append(m.Condition.As(ctx, &condition, basetypes.ObjectAsOptions{})...)
		if !condition.RequestIP.IsNull() && !condition.RequestIP.IsUnknown() {
			var ip tokenIPModel
			diags.Append(condition.RequestIP.As(ctx, &ip, basetypes.ObjectAsOptions{})...)
			fields.Condition = &apiTokenCondition{RequestIP: &apiTokenIP{
				In: readCIDRs(ctx, ip.In, "in", &diags), NotIn: readCIDRs(ctx, ip.NotIn, "not_in", &diags),
			}}
		}
	}
	return fields, diags
}

func parseTime(value types.String, field string, diags *diag.Diagnostics) *time.Time {
	if !known(value) {
		return nil
	}
	t, err := time.Parse(time.RFC3339, value.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root(field), "Invalid timestamp", "Use an RFC3339 timestamp.")
		return nil
	}
	return &t
}

func readCIDRs(ctx context.Context, list types.List, field string, diags *diag.Diagnostics) []string {
	if list.IsNull() || list.IsUnknown() {
		return nil
	}
	var values []types.String
	diags.Append(list.ElementsAs(ctx, &values, false)...)
	result := make([]string, 0, len(values))
	for i, value := range values {
		if value.IsNull() {
			diags.AddAttributeError(path.Root("condition").AtName("request_ip").AtName(field).AtListIndex(i), "Missing IP range", "Remove the list item or provide an IPv4 or IPv6 CIDR.")
			continue
		}
		if value.IsUnknown() {
			continue
		}
		_, err := netip.ParsePrefix(value.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("condition").AtName("request_ip").AtName(field).AtListIndex(i), "Invalid IP range", "Use IPv4 or IPv6 CIDR notation.")
			continue
		}
		result = append(result, value.ValueString())
	}
	return result
}

func (r *accountTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m accountTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fields, diags := m.requestFields(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	params := accounts.TokenNewParams{
		AccountID: cloudflare.F(m.AccountID.ValueString()), Name: cloudflare.F(fields.Name),
		Policies: cloudflare.Raw[[]shared.TokenPolicyParam](fields.Policies),
	}
	if fields.Condition != nil {
		params.Condition = cloudflare.Raw[accounts.TokenNewParamsCondition](fields.Condition)
	}
	if fields.NotBefore != nil {
		params.NotBefore = cloudflare.F(*fields.NotBefore)
	}
	if fields.ExpiresOn != nil {
		params.ExpiresOn = cloudflare.F(*fields.ExpiresOn)
	}
	out, err := r.api.New(ctx, params, option.WithMaxRetries(0))
	if err != nil {
		resp.Diagnostics.AddError("Could not create account API token", accountTokenError(err, true))
		return
	}
	if out == nil || !accountIDPattern.MatchString(out.ID) || out.Value == "" || out.Status == "" {
		resp.Diagnostics.AddError("Incomplete token creation response", "Check Cloudflare for an issued token before attempting creation again.")
		return
	}
	m.ID, m.Value, m.Status = types.StringValue(out.ID), types.StringValue(out.Value), types.StringValue(string(out.Status))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *accountTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m accountTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.api.Get(ctx, m.ID.ValueString(), accounts.TokenGetParams{AccountID: cloudflare.F(m.AccountID.ValueString())})
	if statusCode(err) == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read account API token", accountTokenError(err, false))
		return
	}
	var record tokenRecord
	if out == nil || out.ID != m.ID.ValueString() || json.Unmarshal([]byte(out.JSON.RawJSON()), &record) != nil || record.Status == "" || len(record.Policies) == 0 {
		resp.Diagnostics.AddError("Incomplete token response", "Cloudflare did not return the requested token and its configuration.")
		return
	}
	if value := m.Value.ValueString(); value != "" && out.TokenSuffix != "" && !strings.HasSuffix(value, out.TokenSuffix) {
		resp.Diagnostics.AddWarning("Account API token was rotated outside Terraform", "The saved token value may be unusable. Move consumers to a newly issued token and revoke the old token after verification.")
	}
	for i := range record.Policies {
		record.Policies[i].Resources = json.RawMessage(canonicalResources(record.Policies[i].Resources))
	}
	resp.Diagnostics.Append(m.setFields(ctx, record)...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (m *accountTokenModel) setFields(ctx context.Context, record tokenRecord) diag.Diagnostics {
	var diags diag.Diagnostics
	s := accountTokenSchema()
	policyType := s.Attributes["policies"].GetType().(basetypes.ListType)
	conditionType := s.Attributes["condition"].GetType().(basetypes.ObjectType)
	prior, _ := m.requestFields(ctx)
	m.Name, m.Status = types.StringValue(record.Name), types.StringValue(record.Status)
	m.ExpiresOn = refreshTime(m.ExpiresOn, record.ExpiresOn)
	m.NotBefore = refreshTime(m.NotBefore, record.NotBefore)

	if !bytes.Equal(policyFingerprint(prior.Policies), policyFingerprint(record.Policies)) {
		var policies []tokenPolicyModel
		groupsType := policyType.ElemType.(basetypes.ObjectType).AttrTypes["permission_groups"].(basetypes.ListType)
		for _, policy := range record.Policies {
			groups := make([]tokenPermissionModel, 0, len(policy.PermissionGroups))
			for _, g := range policy.PermissionGroups {
				groups = append(groups, tokenPermissionModel{ID: types.StringValue(g.ID)})
			}
			list, d := types.ListValueFrom(ctx, groupsType.ElemType, groups)
			diags.Append(d...)
			policies = append(policies, tokenPolicyModel{
				Effect: types.StringValue(policy.Effect), PermissionGroups: list, Resources: types.StringValue(string(policy.Resources)),
			})
		}
		list, d := types.ListValueFrom(ctx, policyType.ElemType, policies)
		diags.Append(d...)
		m.Policies = list
	}

	if len(m.Condition.AttributeTypes(ctx)) == 0 || !sameCondition(prior.Condition, record.Condition) {
		condition := record.Condition
		m.Condition = types.ObjectNull(conditionType.AttrTypes)
		if condition != nil && condition.RequestIP != nil {
			ipType := conditionType.AttrTypes["request_ip"].(basetypes.ObjectType)
			in, d := types.ListValueFrom(ctx, types.StringType, condition.RequestIP.In)
			diags.Append(d...)
			notIn, d := types.ListValueFrom(ctx, types.StringType, condition.RequestIP.NotIn)
			diags.Append(d...)
			ip, d := types.ObjectValueFrom(ctx, ipType.AttrTypes, tokenIPModel{In: in, NotIn: notIn})
			diags.Append(d...)
			m.Condition, d = types.ObjectValueFrom(ctx, conditionType.AttrTypes, tokenConditionModel{RequestIP: ip})
			diags.Append(d...)
		}
	}
	return diags
}

func canonicalResources(raw json.RawMessage) string {
	var resources map[string]any
	if json.Unmarshal(raw, &resources) != nil {
		return string(raw)
	}
	value, err := json.Marshal(resources)
	if err != nil {
		return string(raw)
	}
	return string(value)
}

func policyFingerprint(policies []apiTokenPolicy) []byte {
	values := make([]string, 0, len(policies))
	for _, policy := range policies {
		groups := make([]string, 0, len(policy.PermissionGroups))
		for _, group := range policy.PermissionGroups {
			groups = append(groups, group.ID)
		}
		sort.Strings(groups)
		value, _ := json.Marshal([]any{policy.Effect, groups, canonicalResources(policy.Resources)})
		values = append(values, string(value))
	}
	sort.Strings(values)
	out, _ := json.Marshal(values)
	return out
}

func sameCondition(a, b *apiTokenCondition) bool {
	normalize := func(c *apiTokenCondition) []byte {
		if c == nil || c.RequestIP == nil || len(c.RequestIP.In)+len(c.RequestIP.NotIn) == 0 {
			return nil
		}
		in := append([]string{}, c.RequestIP.In...)
		notIn := append([]string{}, c.RequestIP.NotIn...)
		sort.Strings(in)
		sort.Strings(notIn)
		raw, _ := json.Marshal(apiTokenIP{In: in, NotIn: notIn})
		return raw
	}
	return bytes.Equal(normalize(a), normalize(b))
}

func sameTokenFields(a, b tokenFields) bool {
	return a.Name == b.Name && sameTime(a.ExpiresOn, b.ExpiresOn) && sameTime(a.NotBefore, b.NotBefore) &&
		bytes.Equal(policyFingerprint(a.Policies), policyFingerprint(b.Policies)) && sameCondition(a.Condition, b.Condition)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func refreshTime(prior types.String, next *time.Time) types.String {
	if next == nil || next.IsZero() {
		return types.StringNull()
	}
	if t, err := time.Parse(time.RFC3339, prior.ValueString()); err == nil && t.Equal(*next) {
		return prior
	}
	return types.StringValue(next.UTC().Format(time.RFC3339Nano))
}

func (r *accountTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state accountTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fields, diags := plan.requestFields(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	prior, priorDiags := state.requestFields(ctx)
	if !priorDiags.HasError() && sameTokenFields(prior, fields) {
		plan.ID, plan.Value, plan.Status = state.ID, state.Value, state.Status
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	body := shared.TokenParam{Name: cloudflare.F(fields.Name), Policies: cloudflare.Raw[[]shared.TokenPolicyParam](fields.Policies)}
	if state.Status.ValueString() == string(shared.TokenStatusDisabled) {
		body.Status = cloudflare.F(shared.TokenStatusDisabled)
	}
	if fields.Condition != nil {
		body.Condition = cloudflare.Raw[shared.TokenConditionParam](fields.Condition)
	} else if !state.Condition.IsNull() {
		body.Condition = cloudflare.Null[shared.TokenConditionParam]()
	}
	if fields.NotBefore != nil {
		body.NotBefore = cloudflare.F(*fields.NotBefore)
	} else if known(state.NotBefore) {
		body.NotBefore = cloudflare.Null[time.Time]()
	}
	if fields.ExpiresOn != nil {
		body.ExpiresOn = cloudflare.F(*fields.ExpiresOn)
	} else if known(state.ExpiresOn) {
		body.ExpiresOn = cloudflare.Null[time.Time]()
	}
	params := accounts.TokenUpdateParams{AccountID: cloudflare.F(state.AccountID.ValueString()), Token: body}
	out, err := r.api.Update(ctx, state.ID.ValueString(), params, option.WithMaxRetries(0))
	if err != nil {
		resp.Diagnostics.AddError("Could not update account API token", accountTokenError(err, false))
		return
	}
	if out == nil || out.ID != state.ID.ValueString() || out.Status == "" {
		resp.Diagnostics.AddError("Incomplete token update response", "Refresh the token with a new plan before retrying.")
		return
	}
	plan.ID, plan.Value, plan.Status = state.ID, state.Value, types.StringValue(string(out.Status))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *accountTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m accountTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.api.Delete(ctx, m.ID.ValueString(), accounts.TokenDeleteParams{AccountID: cloudflare.F(m.AccountID.ValueString())}, option.WithMaxRetries(0))
	if statusCode(err) == http.StatusNotFound {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not revoke account API token", accountTokenError(err, false))
	} else if out == nil || out.ID != m.ID.ValueString() {
		resp.Diagnostics.AddError("Unconfirmed token revocation", "Check the token's status in Cloudflare before retrying.")
	}
}

func (*accountTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	account, id, ok := splitImportID(req.ID, accountIDPattern)
	if !ok {
		resp.Diagnostics.AddError("Invalid account token import ID", "Use ACCOUNT_ID/TOKEN_ID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("account_id"), account)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func accountTokenError(err error, creating bool) string {
	if statusCode(err) == http.StatusForbidden {
		return "The credential lacks permission to manage this account's API tokens. Check Account API Tokens Read or Write for the operation."
	}
	if creating && (statusCode(err) == 0 || statusCode(err) >= 500 || statusCode(err) == http.StatusRequestTimeout) {
		return "Cloudflare might have created a token before the request failed. Check the account's token list before retrying. Revoke a confirmed unused token whose value was lost."
	}
	return apiErrorDetail(err)
}
