package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	cloudflare "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/accounts"
	"github.com/cloudflare/cloudflare-go/v7/billing"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type controlsProvider struct {
	version string
}

type providerConfig struct {
	APIToken types.String `tfsdk:"api_token"`
}

type client struct {
	spend *billing.SpendPolicyService
	token *accounts.TokenService
}

const apiEndpoint = "https://api.cloudflare.com/client/v4/"

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &controlsProvider{version: version} }
}

func (p *controlsProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "cloudflarecontrols"
	resp.Version = p.version
}

func (p *controlsProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage Cloudflare spending policies and account-owned API tokens alongside the Cloudflare provider.",
		Attributes: map[string]schema.Attribute{
			"api_token": schema.StringAttribute{
				Description: "Cloudflare API token. Defaults to CLOUDFLARE_API_TOKEN. Use a credential scoped to the resources this configuration manages.",
				Optional:    true, Sensitive: true,
			},
		},
	}
}

func (p *controlsProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerConfig
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.APIToken.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_token"), "Unknown Cloudflare API token", "Provide an authentication token known before planning.")
		return
	}

	token := config.APIToken.ValueString()
	if config.APIToken.IsNull() {
		token = os.Getenv("CLOUDFLARE_API_TOKEN")
	}
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		resp.Diagnostics.AddAttributeError(path.Root("api_token"), "Invalid Cloudflare API token", "Set CLOUDFLARE_API_TOKEN or set the provider's api_token to a nonempty token without whitespace.")
		return
	}

	resp.ResourceData = newClient(token, apiEndpoint, http.DefaultTransport)
}

func newClient(token, endpoint string, transport http.RoundTripper) *client {
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("Cloudflare API redirects are disabled")
		},
	}
	opts := []option.RequestOption{
		option.WithBaseURL(endpoint),
		option.WithHTTPClient(httpClient),
		option.WithAPIToken(token),
	}
	return &client{spend: billing.NewSpendPolicyService(opts...), token: accounts.NewTokenService(opts...)}
}

func (p *controlsProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{newSpendPolicyResource, newAccountTokenResource}
}

func (p *controlsProvider) DataSources(context.Context) []func() datasource.DataSource {
	return nil
}

var accountIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var policyIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func splitImportID(id string, idPattern *regexp.Regexp) (string, string, bool) {
	account, object, found := strings.Cut(id, "/")
	return account, object, found && accountIDPattern.MatchString(account) && idPattern.MatchString(object)
}

func statusCode(err error) int {
	var apiError *cloudflare.Error
	if errors.As(err, &apiError) {
		return apiError.StatusCode
	}
	return 0
}

func apiErrorDetail(err error) string {
	if code := statusCode(err); code != 0 {
		return fmt.Sprintf("Cloudflare returned HTTP %d.", code)
	}
	return "The request did not complete. Check your connection and retry after verifying the current Cloudflare configuration."
}
