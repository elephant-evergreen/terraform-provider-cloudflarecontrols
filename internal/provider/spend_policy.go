package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	cloudflare "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/billing"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type spendPolicyResource struct {
	api *billing.SpendPolicyService
}

type spendPolicyModel struct {
	AccountID         types.String `tfsdk:"account_id"`
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	ProductID         types.String `tfsdk:"product_id"`
	Action            types.String `tfsdk:"action"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	ThresholdCents    types.Int64  `tfsdk:"threshold_cents"`
	NotificationEmail types.String `tfsdk:"notification_email"`
	Version           types.Int64  `tfsdk:"version"`
}

var (
	_ resource.ResourceWithConfigure      = (*spendPolicyResource)(nil)
	_ resource.ResourceWithImportState    = (*spendPolicyResource)(nil)
	_ resource.ResourceWithValidateConfig = (*spendPolicyResource)(nil)
)

func newSpendPolicyResource() resource.Resource { return &spendPolicyResource{} }

func (*spendPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_spend_policy"
}

func (*spendPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage a spending limit or notification for Workers or Durable Objects. An enabled usage block can interrupt product operations across the account.",
		Attributes: map[string]schema.Attribute{
			"account_id":         schema.StringAttribute{Description: "Cloudflare account ID. Changing it replaces the policy.", Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"id":                 schema.StringAttribute{Description: "Spend policy UUID.", Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":               schema.StringAttribute{Description: "Display name, 1–200 bytes after trimming whitespace.", Required: true},
			"product_id":         schema.StringAttribute{Description: "workers or durable_objects.", Required: true},
			"action":             schema.StringAttribute{Description: "usage_block stops supported operations. notification sends an email.", Required: true},
			"enabled":            schema.BoolAttribute{Description: "Whether Cloudflare evaluates the policy.", Required: true},
			"threshold_cents":    schema.Int64Attribute{Description: "Positive spend threshold in USD cents. The period is the product billing period.", Required: true},
			"notification_email": schema.StringAttribute{Description: "Bare recipient address. Required for notification.", Optional: true, Sensitive: true},
			"version":            schema.Int64Attribute{Description: "Cloudflare's configuration version, used to detect concurrent edits.", Computed: true},
		},
	}
}

func (r *spendPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider configuration", "The Cloudflare client was not configured.")
		return
	}
	r.api = c.spend
}

func (*spendPolicyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var model spendPolicyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if known(model.AccountID) && !accountIDPattern.MatchString(model.AccountID.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("account_id"), "Invalid account ID", "Use the 32-character lowercase hexadecimal Cloudflare account ID.")
	}
	if known(model.Name) {
		length := len(strings.TrimSpace(model.Name.ValueString()))
		if length < 1 || length > 200 {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid name", "Use a name with 1–200 bytes after trimming whitespace.")
		}
	}
	product := model.ProductID.ValueString()
	if known(model.ProductID) && product != "workers" && product != "durable_objects" {
		resp.Diagnostics.AddAttributeError(path.Root("product_id"), "Invalid product", "Set product_id to workers or durable_objects.")
	}
	action := model.Action.ValueString()
	if known(model.Action) && action != "usage_block" && action != "notification" {
		resp.Diagnostics.AddAttributeError(path.Root("action"), "Invalid action", "Set action to usage_block or notification.")
	}
	if !model.ThresholdCents.IsNull() && !model.ThresholdCents.IsUnknown() && model.ThresholdCents.ValueInt64() < 1 {
		resp.Diagnostics.AddAttributeError(path.Root("threshold_cents"), "Invalid threshold", "Set a positive integer amount in USD cents.")
	}
	if known(model.NotificationEmail) {
		email, err := mail.ParseAddress(model.NotificationEmail.ValueString())
		if err != nil || email.Address != model.NotificationEmail.ValueString() || email.Name != "" {
			resp.Diagnostics.AddAttributeError(path.Root("notification_email"), "Invalid notification address", "Use a bare email address.")
		}
	} else if model.NotificationEmail.IsNull() && action == "notification" {
		resp.Diagnostics.AddAttributeError(path.Root("notification_email"), "Missing notification address", "An email address is required for a notification policy.")
	}
}

func known(value types.String) bool { return !value.IsNull() && !value.IsUnknown() }

func (r *spendPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m spendPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	params := billing.SpendPolicyNewParams{
		AccountID: cloudflare.F(m.AccountID.ValueString()), Name: cloudflare.F(m.Name.ValueString()),
		ProductID: cloudflare.F(billing.SpendPolicyNewParamsProductID(m.ProductID.ValueString())),
		Action:    cloudflare.F(billing.SpendPolicyNewParamsAction(m.Action.ValueString())),
		Enabled:   cloudflare.F(m.Enabled.ValueBool()), ThresholdCents: cloudflare.F(m.ThresholdCents.ValueInt64()),
	}
	if known(m.NotificationEmail) {
		params.NotificationEmail = cloudflare.F(m.NotificationEmail.ValueString())
	}
	out, err := r.api.New(ctx, params, option.WithMaxRetries(0))
	if err != nil {
		resp.Diagnostics.AddError("Could not create spend policy", spendPolicyError(err, true))
		return
	}
	if out == nil || !policyIDPattern.MatchString(out.ID) || out.Version < 1 {
		resp.Diagnostics.AddError("Incomplete spend policy response", "Check the account's existing policies before retrying creation.")
		return
	}
	m.ID, m.Version = types.StringValue(out.ID), types.Int64Value(out.Version)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *spendPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m spendPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.api.Get(ctx, m.ID.ValueString(), billing.SpendPolicyGetParams{AccountID: cloudflare.F(m.AccountID.ValueString())})
	if statusCode(err) == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not read spend policy", spendPolicyError(err, false))
		return
	}
	if out == nil || out.ID != m.ID.ValueString() || out.Version < 1 {
		resp.Diagnostics.AddError("Incomplete spend policy response", "Cloudflare did not return the requested policy and its version.")
		return
	}
	m.Name = types.StringValue(out.Name)
	m.ProductID = types.StringValue(string(out.ProductID))
	m.Action = types.StringValue(string(out.Action))
	m.Enabled = types.BoolValue(out.Enabled)
	m.ThresholdCents = types.Int64Value(out.ThresholdCents)
	m.NotificationEmail = types.StringNull()
	if out.NotificationEmail != "" {
		m.NotificationEmail = types.StringValue(out.NotificationEmail)
	}
	m.Version = types.Int64Value(out.Version)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *spendPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state spendPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Version.ValueInt64() < 1 {
		resp.Diagnostics.AddError("Missing spend policy version", "Refresh the policy with a new plan and retry.")
		return
	}
	params := billing.SpendPolicyUpdateParams{
		AccountID: cloudflare.F(state.AccountID.ValueString()), Name: cloudflare.F(plan.Name.ValueString()),
		ProductID: cloudflare.F(billing.SpendPolicyUpdateParamsProductID(plan.ProductID.ValueString())),
		Action:    cloudflare.F(billing.SpendPolicyUpdateParamsAction(plan.Action.ValueString())),
		Enabled:   cloudflare.F(plan.Enabled.ValueBool()), ThresholdCents: cloudflare.F(plan.ThresholdCents.ValueInt64()),
		IfMatch: cloudflare.F(strconv.FormatInt(state.Version.ValueInt64(), 10)),
	}
	if known(plan.NotificationEmail) {
		params.NotificationEmail = cloudflare.F(plan.NotificationEmail.ValueString())
	}
	out, err := r.api.Update(ctx, state.ID.ValueString(), params, option.WithMaxRetries(0))
	if err != nil {
		resp.Diagnostics.AddError("Could not update spend policy", spendPolicyError(err, false))
		return
	}
	if out == nil || out.ID != state.ID.ValueString() || out.Version <= state.Version.ValueInt64() {
		resp.Diagnostics.AddError("Incomplete spend policy response", "Refresh the policy with a new plan before retrying.")
		return
	}
	plan.ID, plan.Version = state.ID, types.Int64Value(out.Version)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *spendPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m spendPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Version.ValueInt64() < 1 {
		resp.Diagnostics.AddError("Missing spend policy version", "Refresh the policy with a new plan and retry.")
		return
	}
	out, err := r.api.Delete(ctx, m.ID.ValueString(), billing.SpendPolicyDeleteParams{
		AccountID: cloudflare.F(m.AccountID.ValueString()), IfMatch: cloudflare.F(strconv.FormatInt(m.Version.ValueInt64(), 10)),
	}, option.WithMaxRetries(0))
	if statusCode(err) == http.StatusNotFound {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Could not delete spend policy", spendPolicyError(err, false))
	} else if out == nil || out.ID != m.ID.ValueString() {
		resp.Diagnostics.AddError("Unconfirmed spend policy deletion", "Check the policy in Cloudflare before retrying.")
	}
}

func (*spendPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	account, id, ok := splitImportID(req.ID, policyIDPattern)
	if !ok {
		resp.Diagnostics.AddError("Invalid spend policy import ID", "Use ACCOUNT_ID/POLICY_UUID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("account_id"), account)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func spendPolicyError(err error, creating bool) string {
	switch statusCode(err) {
	case http.StatusConflict:
		return "The account already has an enabled block for this product. Import that policy to manage it."
	case http.StatusPreconditionFailed, http.StatusPreconditionRequired:
		return "The policy version no longer matches. Create and review a fresh plan before retrying."
	case http.StatusForbidden:
		return "The credential lacks permission to access this account's billing settings."
	}
	if creating && (statusCode(err) == 0 || statusCode(err) >= 500 || statusCode(err) == http.StatusRequestTimeout) {
		return fmt.Sprintf("%s Check for an existing policy before retrying creation.", apiErrorDetail(err))
	}
	return apiErrorDetail(err)
}
