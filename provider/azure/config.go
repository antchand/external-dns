/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// config represents common config items for Azure DNS and Azure Private DNS
type config struct {
	Cloud                        string `json:"cloud"                        yaml:"cloud"`
	TenantID                     string `json:"tenantId"                     yaml:"tenantId"`
	SubscriptionID               string `json:"subscriptionId"               yaml:"subscriptionId"`
	ResourceGroup                string `json:"resourceGroup"                yaml:"resourceGroup"`
	Location                     string `json:"location"                     yaml:"location"`
	ClientID                     string `json:"aadClientId"                  yaml:"aadClientId"`
	ClientSecret                 string `json:"aadClientSecret"              yaml:"aadClientSecret"`
	UseManagedIdentityExtension  bool   `json:"useManagedIdentityExtension"  yaml:"useManagedIdentityExtension"`
	UseWorkloadIdentityExtension bool   `json:"useWorkloadIdentityExtension" yaml:"useWorkloadIdentityExtension"`
	UserAssignedIdentityID       string `json:"userAssignedIdentityID"       yaml:"userAssignedIdentityID"`
	ActiveDirectoryAuthorityHost string `json:"activeDirectoryAuthorityHost" yaml:"activeDirectoryAuthorityHost"`
	ResourceManagerAudience      string `json:"resourceManagerAudience"      yaml:"resourceManagerAudience"`
	ResourceManagerEndpoint      string `json:"resourceManagerEndpoint"      yaml:"resourceManagerEndpoint"`
}

func getConfig(configFile, subscriptionID, resourceGroup, userAssignedIdentityClientID, activeDirectoryAuthorityHost string) (*config, error) {
	contents, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read Azure config file '%s': %w", configFile, err)
	}
	cfg := &config{}
	if err := json.Unmarshal(contents, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse Azure config file '%s': %w", configFile, err)
	}
	// If a subscription ID was given, override what was present in the config file
	if subscriptionID != "" {
		cfg.SubscriptionID = subscriptionID
	}
	// If a resource group was given, override what was present in the config file
	if resourceGroup != "" {
		cfg.ResourceGroup = resourceGroup
	}
	// If userAssignedIdentityClientID is provided explicitly, override existing one in config file
	if userAssignedIdentityClientID != "" {
		cfg.UserAssignedIdentityID = userAssignedIdentityClientID
	}
	// If activeDirectoryAuthorityHost is provided explicitly, override existing one in config file
	if activeDirectoryAuthorityHost != "" {
		cfg.ActiveDirectoryAuthorityHost = activeDirectoryAuthorityHost
	}
	return cfg, nil
}

// ctxKey is a type for context keys
// This is used to avoid collisions with other packages that may use the same key in the context.
type ctxKey string

const (
	// Context key for request ID
	clientRequestIDKey ctxKey = "client-request-id"
	// Azure API Headers
	msRequestIDHeader          = "x-ms-request-id"
	msCorrelationRequestHeader = "x-ms-correlation-request-id"
	msClientRequestIDHeader    = "x-ms-client-request-id"
)

// customHeaderPolicy adds UUID to request headers
type customHeaderPolicy struct{}

func (p *customHeaderPolicy) Do(req *policy.Request) (*http.Response, error) {
	id := req.Raw().Header.Get(msClientRequestIDHeader)
	if id == "" {
		id = uuid.New().String()
		req.Raw().Header.Set(msClientRequestIDHeader, id)
		newCtx := context.WithValue(req.Raw().Context(), clientRequestIDKey, id)
		*req.Raw() = *req.Raw().WithContext(newCtx)
	}
	return req.Next()
}
func CustomHeaderPolicynew() policy.Policy { return &customHeaderPolicy{} }

// getCredentials retrieves Azure API credentials.
func getCredentials(cfg config, maxRetries int) (azcore.TokenCredential, *arm.ClientOptions, error) {
	cloudCfg, err := getCloudConfiguration(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get cloud configuration: %w", err)
	}
	clientOpts := azcore.ClientOptions{
		Cloud: cloudCfg,
		Retry: policy.RetryOptions{
			MaxRetries: int32(maxRetries),
		},
		Logging: policy.LogOptions{
			AllowedHeaders: []string{
				msRequestIDHeader,
				msCorrelationRequestHeader,
				msClientRequestIDHeader,
			},
		},
		PerCallPolicies: []policy.Policy{
			CustomHeaderPolicynew(),
		},
	}
	log.Debugf("Configured Azure client with maxRetries: %d", clientOpts.Retry.MaxRetries)
	armClientOpts := &arm.ClientOptions{
		ClientOptions: clientOpts,
	}

	// Try to retrieve token with service principal credentials.
	// Try to use service principal first, some AKS clusters are in an intermediate state that `UseManagedIdentityExtension` is `true`
	// and service principal exists. In this case, we still want to use service principal to authenticate.
	if len(cfg.ClientID) > 0 &&
		len(cfg.ClientSecret) > 0 &&
		// due to some historical reason, for pure MSI cluster,
		// they will use "msi" as placeholder in azure.json.
		// In this case, we shouldn't try to use SPN to authenticate.
		!strings.EqualFold(cfg.ClientID, "msi") &&
		!strings.EqualFold(cfg.ClientSecret, "msi") {
		log.Info("Using client_id+client_secret to retrieve access token for Azure API.")
		opts := &azidentity.ClientSecretCredentialOptions{
			ClientOptions: clientOpts,
		}
		cred, err := azidentity.NewClientSecretCredential(cfg.TenantID, cfg.ClientID, cfg.ClientSecret, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create service principal token: %w", err)
		}
		return cred, armClientOpts, nil
	}

	// Try to retrieve token with Workload Identity.
	if cfg.UseWorkloadIdentityExtension {
		log.Info("Using workload identity extension to retrieve access token for Azure API.")

		wiOpt := azidentity.WorkloadIdentityCredentialOptions{
			ClientOptions: clientOpts,
			// In a standard scenario, Client ID and Tenant ID are expected to be read from environment variables.
			// Though, in certain cases, it might be important to have an option to override those (e.g. when AZURE_TENANT_ID is not set
			// through a webhook or azure.workload.identity/client-id service account annotation is absent). When any of those values are
			// empty in our config, they will automatically be read from environment variables by azidentity
			TenantID: cfg.TenantID,
			ClientID: cfg.ClientID,
		}

		cred, err := azidentity.NewWorkloadIdentityCredential(&wiOpt)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create a workload identity token: %w", err)
		}

		return cred, armClientOpts, nil
	}

	// Try to retrieve token with MSI.
	if cfg.UseManagedIdentityExtension {
		log.Info("Using managed identity extension to retrieve access token for Azure API.")
		msiOpt := azidentity.ManagedIdentityCredentialOptions{
			ClientOptions: clientOpts,
		}
		if cfg.UserAssignedIdentityID != "" {
			msiOpt.ID = azidentity.ClientID(cfg.UserAssignedIdentityID)
		}
		cred, err := azidentity.NewManagedIdentityCredential(&msiOpt)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create the managed service identity token: %w", err)
		}
		return cred, armClientOpts, nil
	}

	return nil, nil, fmt.Errorf("no credentials provided for Azure API")
}

func getCloudConfiguration(cfg config) (cloud.Configuration, error) {
	name := strings.ToUpper(cfg.Cloud)
	switch name {
	case "AZURECLOUD", "AZUREPUBLICCLOUD", "":
		return cloud.AzurePublic, nil
	case "AZUREUSGOVERNMENT", "AZUREUSGOVERNMENTCLOUD":
		return cloud.AzureGovernment, nil
	case "AZURECHINACLOUD":
		return cloud.AzureChina, nil
	case "AZURESTACKCLOUD":
		return cloud.Configuration{
			ActiveDirectoryAuthorityHost: cfg.ActiveDirectoryAuthorityHost,
			Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
				cloud.ResourceManager: {
					Audience: cfg.ResourceManagerAudience,
					Endpoint: cfg.ResourceManagerEndpoint,
				},
			},
		}, nil
	}
	return cloud.Configuration{}, fmt.Errorf("unknown cloud name: %s", name)
}
