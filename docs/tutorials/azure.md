# Azure DNS

This tutorial describes how to setup ExternalDNS for [Azure DNS](https://azure.microsoft.com/services/dns/) with [Azure Kubernetes Service](https://docs.microsoft.com/azure/aks/).

Make sure to use **>=0.11.0** version of ExternalDNS for this tutorial.

This tutorial uses [Azure CLI 2.0](https://docs.microsoft.com/en-us/cli/azure/install-azure-cli) for all
Azure commands and assumes that the Kubernetes cluster was created via Azure Container Services and `kubectl` commands
are being run on an orchestration node.

## Creating an Azure DNS zone

The Azure provider for ExternalDNS will find suitable zones for domains it manages; it will not automatically create zones.

For this tutorial, we will create a Azure resource group named `MyDnsResourceGroup` that can easily be deleted later:

```bash
az group create --name "MyDnsResourceGroup" --location "eastus"
```

Substitute a more suitable location for the resource group if desired.

Next, create a Azure DNS zone for `example.com`:

```bash
az network dns zone create --resource-group "MyDnsResourceGroup" --name "example.com"
```

Substitute a domain you own for `example.com` if desired.

If using your own domain that was registered with a third-party domain registrar, you should point your domain's name servers to the values in the `nameServers` field from the JSON data returned by the `az network dns zone create` command. Please consult your registrar's documentation on how to do that.

### Internal Load Balancer

To create internal load balancers, one can set the annotation `service.beta.kubernetes.io/azure-load-balancer-internal` to `true` on the resource.
**Note**: AKS cluster's control plane managed identity needs to be granted `Network Contributor` role to update the subnet. For more details refer to [Use an internal load balancer with Azure Kubernetes Service (AKS)](https://learn.microsoft.com/en-us/azure/aks/internal-lb)

## Configuration file

The azure provider will reference a configuration file called `azure.json`.  The preferred way to inject the configuration file is by using a Kubernetes secret. The secret should contain an object named `azure.json` with content similar to this:

```json
{
  "tenantId": "01234abc-de56-ff78-abc1-234567890def",
  "subscriptionId": "01234abc-de56-ff78-abc1-234567890def",
  "resourceGroup": "MyDnsResourceGroup",
  "aadClientId": "01234abc-de56-ff78-abc1-234567890def",
  "aadClientSecret": "uKiuXeiwui4jo9quae9o"
}
```

The following fields are used:

* `tenantId` (**required**) - run `az account show --query "tenantId"` or by selecting Azure Active Directory in the Azure Portal and checking the _Directory ID_ under Properties.
* `subscriptionId` (**required**) - run `az account show --query "id"` or by selecting Subscriptions in the Azure Portal.
* `resourceGroup` (**required**) is the Resource Group created in a previous step that contains the Azure DNS Zone.
* `aadClientID` is associated with the Service Principal. This is used with Service Principal or Workload Identity methods documented in the next section.
* `aadClientSecret` is associated with the Service Principal. This is only used with Service Principal method documented in the next section.
* `useManagedIdentityExtension` - this is set to `true` if you use either AKS Kubelet Identity or AAD Pod Identities methods documented in the next section.
* `userAssignedIdentityID` - this contains the client id from the Managed identity when using the AAD Pod Identities method documented in the next setion.
* `activeDirectoryAuthorityHost` - this contains the URI to override the default Azure Active Directory authority endpoint.
  This is useful for Azure Stack Cloud deployments or custom environments.
* `useWorkloadIdentityExtension` - this is set to `true` if you use Workload Identity method documented in the next section.
* `ResourceManagerAudience` - this specifies the audience for the Azure Resource Manager service when using Azure Stack Cloud. This is required for Azure Stack Cloud deployments to authenticate with the correct Resource Manager endpoint.
* `ResourceManagerEndpoint` - this specifies the endpoint URL for the Azure Resource Manager service when using Azure Stack Cloud. This is required for Azure Stack Cloud deployments to point to the correct Resource Manager instance.

The Azure DNS provider expects, by default, that the configuration file is at `/etc/kubernetes/azure.json`.  This can be overridden with the `--azure-config-file` option when starting ExternalDNS.

## Permissions to modify DNS zone

ExternalDNS needs permissions to make changes to the Azure DNS zone. There are four ways configure the access needed:

* [Service Principal](#service-principal)
* [Managed Identity Using AKS Kubelet Identity](#managed-identity-using-aks-kubelet-identity)
* [Managed Identity Using AAD Pod Identities](#managed-identity-using-aad-pod-identities)
* [Managed Identity Using Workload Identity](#managed-identity-using-workload-identity)

### Service Principal

These permissions are defined in a Service Principal that should be made available to ExternalDNS as a configuration file `azure.json`.

#### Creating a service principal

A Service Principal with a minimum access level of `DNS Zone Contributor` or `Contributor` to the DNS zone(s) and `Reader` to the resource group containing the Azure DNS zone(s) is necessary for ExternalDNS to be able to edit DNS records.
However, other more permissive access levels will work too (e.g. `Contributor` to the resource group or the whole subscription).

This is an Azure CLI example on how to query the Azure API for the information required for the Resource Group and DNS zone you would have already created in previous steps (requires `azure-cli` and `jq`)

```bash
$ EXTERNALDNS_NEW_SP_NAME="ExternalDnsServicePrincipal" # name of the service principal
$ AZURE_DNS_ZONE_RESOURCE_GROUP="MyDnsResourceGroup" # name of resource group where dns zone is hosted
$ AZURE_DNS_ZONE="example.com" # DNS zone name like example.com or sub.example.com

# Create the service principal
$ DNS_SP=$(az ad sp create-for-rbac --name $EXTERNALDNS_NEW_SP_NAME)
$ EXTERNALDNS_SP_APP_ID=$(echo $DNS_SP | jq -r '.appId')
$ EXTERNALDNS_SP_PASSWORD=$(echo $DNS_SP | jq -r '.password')
```

#### Assign the rights for the service principal

Grant access to Azure DNS zone for the service principal.

```bash
# fetch DNS id used to grant access to the service principal
DNS_ID=$(az network dns zone show --name $AZURE_DNS_ZONE \
 --resource-group $AZURE_DNS_ZONE_RESOURCE_GROUP --query "id" --output tsv)

# 1. as a reader to the resource group
$ az role assignment create --role "Reader" --assignee $EXTERNALDNS_SP_APP_ID --scope $DNS_ID

# 2. as a contributor to DNS Zone itself
$ az role assignment create --role "Contributor" --assignee $EXTERNALDNS_SP_APP_ID --scope $DNS_ID
```

#### Creating a configuration file for the service principal

Create the file `azure.json` with values gather from previous steps.

```bash
cat <<-EOF > /local/path/to/azure.json
{
  "tenantId": "$(az account show --query tenantId -o tsv)",
  "subscriptionId": "$(az account show --query id -o tsv)",
  "resourceGroup": "$AZURE_DNS_ZONE_RESOURCE_GROUP",
  "aadClientId": "$EXTERNALDNS_SP_APP_ID",
  "aadClientSecret": "$EXTERNALDNS_SP_PASSWORD"
}
EOF
```

Use this file to create a Kubernetes secret:

```bash
kubectl create secret generic azure-config-file --namespace "default" --from-file /local/path/to/azure.json
```

### Managed identity using AKS Kubelet identity

The [managed identity](https://docs.microsoft.com/azure/active-directory/managed-identities-azure-resources/overview) that is assigned to the underlying node pool in the AKS cluster can be given permissions to access Azure DNS.
Managed identities are essentially a service principal whose lifecycle is managed, such as deleting the AKS cluster will also delete the service principals associated with the AKS cluster.
The managed identity assigned Kubernetes node pool, or specifically the [VMSS](https://docs.microsoft.com/azure/virtual-machine-scale-sets/overview), is called the Kubelet identity.

The managed identites were previously called MSI (Managed Service Identity) and are enabled by default when creating an AKS cluster.

Note that permissions granted to this identity will be accessible to all containers running inside the Kubernetes cluster, not just the ExternalDNS container(s).

For the managed identity, the contents of `azure.json` should be similar to this:

```json
{
  "tenantId": "01234abc-de56-ff78-abc1-234567890def",
  "subscriptionId": "01234abc-de56-ff78-abc1-234567890def",
  "resourceGroup": "MyDnsResourceGroup",
  "useManagedIdentityExtension": true,
  "userAssignedIdentityID": "01234abc-de56-ff78-abc1-234567890def"
}
```

#### Fetching the Kubelet identity

For this process, you will need to get the kubelet identity:

```bash
$ PRINCIPAL_ID=$(az aks show --resource-group $CLUSTER_GROUP --name $CLUSTERNAME \
  --query "identityProfile.kubeletidentity.objectId" --output tsv)
$ IDENTITY_CLIENT_ID=$(az aks show --resource-group $CLUSTER_GROUP --name $CLUSTERNAME \
  --query "identityProfile.kubeletidentity.clientId" --output tsv)
```

#### Assign rights for the Kubelet identity

Grant access to Azure DNS zone for the kubelet identity.

```bash
$ AZURE_DNS_ZONE="example.com" # DNS zone name like example.com or sub.example.com
$ AZURE_DNS_ZONE_RESOURCE_GROUP="MyDnsResourceGroup" # resource group where DNS zone is hosted

# fetch DNS id used to grant access to the kubelet identity
$ DNS_ID=$(az network dns zone show --name $AZURE_DNS_ZONE \
  --resource-group $AZURE_DNS_ZONE_RESOURCE_GROUP --query "id" --output tsv)

$ az role assignment create --role "DNS Zone Contributor" --assignee $PRINCIPAL_ID --scope $DNS_ID
```

#### Creating a configuration file for the kubelet identity

Create the file `azure.json` with values gather from previous steps.

```bash
cat <<-EOF > /local/path/to/azure.json
{
  "tenantId": "$(az account show --query tenantId -o tsv)",
  "subscriptionId": "$(az account show --query id -o tsv)",
  "resourceGroup": "$AZURE_DNS_ZONE_RESOURCE_GROUP",
  "useManagedIdentityExtension": true,
  "userAssignedIdentityID": "$IDENTITY_CLIENT_ID"
}
EOF
```

Use the `azure.json` file to create a Kubernetes secret:

```bash
kubectl create secret generic azure-config-file --namespace "default" --from-file /local/path/to/azure.json
```

### Managed identity using AAD Pod Identities

For this process, we will create a [managed identity](https://docs.microsoft.com//azure/active-directory/managed-identities-azure-resources/overview) that will be explicitly used by the ExternalDNS container.
This process is similar to Kubelet identity except that this managed identity is not associated with the Kubernetes node pool, but rather associated with explicit ExternalDNS containers.

#### Enable the AAD Pod Identities feature

For this solution, [AAD Pod Identities](https://docs.microsoft.com/azure/aks/use-azure-ad-pod-identity) preview feature can be enabled.  The commands below should do the trick to enable this feature:

```bash
az feature register --name EnablePodIdentityPreview --namespace Microsoft.ContainerService
az feature register --name AutoUpgradePreview --namespace Microsoft.ContainerService
az extension add --name aks-preview
az extension update --name aks-preview
az provider register --namespace Microsoft.ContainerService
```

#### Deploy the AAD Pod Identities service

Once enabled, you can update your cluster and install needed services for the [AAD Pod Identities](https://docs.microsoft.com/azure/aks/use-azure-ad-pod-identity) feature.

```bash
AZURE_AKS_RESOURCE_GROUP="my-aks-cluster-group" # name of resource group where aks cluster was created
AZURE_AKS_CLUSTER_NAME="my-aks-cluster" # name of aks cluster previously created

az aks update --resource-group ${AZURE_AKS_RESOURCE_GROUP} --name ${AZURE_AKS_CLUSTER_NAME} --enable-pod-identity
```

Note that, if you use the default network plugin `kubenet`, then you need to add the command line option `--enable-pod-identity-with-kubenet` to the above command.

#### Creating the managed identity

After this process is finished, create a managed identity.

```bash
$ IDENTITY_RESOURCE_GROUP=$AZURE_AKS_RESOURCE_GROUP # custom group or reuse AKS group
$ IDENTITY_NAME="example-com-identity"

# create a managed identity
$ az identity create --resource-group "${IDENTITY_RESOURCE_GROUP}" --name "${IDENTITY_NAME}"
```

#### Assign rights for the managed identity

Grant access to Azure DNS zone for the managed identity.

```bash
$ AZURE_DNS_ZONE_RESOURCE_GROUP="MyDnsResourceGroup" # name of resource group where dns zone is hosted
$ AZURE_DNS_ZONE="example.com" # DNS zone name like example.com or sub.example.com

# fetch identity client id from managed identity created earlier
$ IDENTITY_CLIENT_ID=$(az identity show --resource-group "${IDENTITY_RESOURCE_GROUP}" \
  --name "${IDENTITY_NAME}" --query "clientId" --output tsv)
# fetch DNS id used to grant access to the managed identity
$ DNS_ID=$(az network dns zone show --name "${AZURE_DNS_ZONE}" \
  --resource-group "${AZURE_DNS_ZONE_RESOURCE_GROUP}" --query "id" --output tsv)

$ az role assignment create --role "DNS Zone Contributor" \
  --assignee "${IDENTITY_CLIENT_ID}" --scope "${DNS_ID}"
```

#### Creating a configuration file for the managed identity

Create the file `azure.json` with the values from previous steps:

```bash
cat <<-EOF > /local/path/to/azure.json
{
  "tenantId": "$(az account show --query tenantId -o tsv)",
  "subscriptionId": "$(az account show --query id -o tsv)",
  "resourceGroup": "$AZURE_DNS_ZONE_RESOURCE_GROUP",
  "useManagedIdentityExtension": true,
  "userAssignedIdentityID": "$IDENTITY_CLIENT_ID"
}
EOF
```

Use the `azure.json` file to create a Kubernetes secret:

```bash
kubectl create secret generic azure-config-file --namespace "default" --from-file /local/path/to/azure.json
```

#### Creating an Azure identity binding

A binding between the managed identity and the ExternalDNS pods needs to be setup by creating `AzureIdentity` and `AzureIdentityBinding` resources.
This will allow appropriately labeled ExternalDNS pods to authenticate using the managed identity.  When AAD Pod Identity feature is enabled from previous steps above, the `az aks pod-identity add` can be used to create these resources:

```bash
$ IDENTITY_RESOURCE_ID=$(az identity show --resource-group ${IDENTITY_RESOURCE_GROUP} \
  --name ${IDENTITY_NAME} --query id --output tsv)

$ az aks pod-identity add --resource-group ${AZURE_AKS_RESOURCE_GROUP}  \
  --cluster-name ${AZURE_AKS_CLUSTER_NAME} --namespace "default" \
  --name "external-dns" --identity-resource-id ${IDENTITY_RESOURCE_ID}
```

This will add something similar to the following resources:

```yaml
apiVersion: aadpodidentity.k8s.io/v1
kind: AzureIdentity
metadata:
  labels:
    addonmanager.kubernetes.io/mode: Reconcile
    kubernetes.azure.com/managedby: aks
  name: external-dns
spec:
  clientID: $IDENTITY_CLIENT_ID
  resourceID: $IDENTITY_RESOURCE_ID
  type: 0
---
apiVersion: aadpodidentity.k8s.io/v1
kind: AzureIdentityBinding
metadata:
  annotations:
  labels:
    addonmanager.kubernetes.io/mode: Reconcile
    kubernetes.azure.com/managedby: aks
  name: external-dns-binding
spec:
  azureIdentity: external-dns
  selector: external-dns
```

#### Update ExternalDNS labels

When deploying ExternalDNS, you want to make sure that deployed pod(s) will have the label `aadpodidbinding: external-dns` to enable AAD Pod Identities. You can patch an existing deployment of ExternalDNS with this command:

```bash
kubectl patch deployment external-dns --namespace "default" --patch \
 '{"spec": {"template": {"metadata": {"labels": {"aadpodidbinding": "external-dns"}}}}}'
```

### Managed identity using Workload Identity

For this process, we will create a [managed identity](https://docs.microsoft.com//azure/active-directory/managed-identities-azure-resources/overview) that will be explicitly used by the ExternalDNS container.
This process is somewhat similar to Pod Identity except that this managed identity is associated with a kubernetes service account.

#### Deploy OIDC issuer and Workload Identity services

Update your cluster to install [OIDC Issuer](https://learn.microsoft.com/en-us/azure/aks/use-oidc-issuer) and [Workload Identity](https://learn.microsoft.com/en-us/azure/aks/workload-identity-deploy-cluster):

```bash
AZURE_AKS_RESOURCE_GROUP="my-aks-cluster-group" # name of resource group where aks cluster was created
AZURE_AKS_CLUSTER_NAME="my-aks-cluster" # name of aks cluster previously created

az aks update --resource-group ${AZURE_AKS_RESOURCE_GROUP} --name ${AZURE_AKS_CLUSTER_NAME} --enable-oidc-issuer --enable-workload-identity
```

#### Create a managed identity

Create a managed identity:

```bash
$ IDENTITY_RESOURCE_GROUP=$AZURE_AKS_RESOURCE_GROUP # custom group or reuse AKS group
$ IDENTITY_NAME="example-com-identity"

# create a managed identity
$ az identity create --resource-group "${IDENTITY_RESOURCE_GROUP}" --name "${IDENTITY_NAME}"
```

#### Assign a role to the managed identity

Grant access to Azure DNS zone for the managed identity:

```bash
$ AZURE_DNS_ZONE_RESOURCE_GROUP="MyDnsResourceGroup" # name of resource group where dns zone is hosted
$ AZURE_DNS_ZONE="example.com" # DNS zone name like example.com or sub.example.com

# fetch identity client id from managed identity created earlier
$ IDENTITY_CLIENT_ID=$(az identity show --resource-group "${IDENTITY_RESOURCE_GROUP}" \
  --name "${IDENTITY_NAME}" --query "clientId" --output tsv)
# fetch DNS id used to grant access to the managed identity
$ DNS_ID=$(az network dns zone show --name "${AZURE_DNS_ZONE}" \
  --resource-group "${AZURE_DNS_ZONE_RESOURCE_GROUP}" --query "id" --output tsv)
$ RESOURCE_GROUP_ID=$(az group show --name "${AZURE_DNS_ZONE_RESOURCE_GROUP}" --query "id" --output tsv)

$ az role assignment create --role "DNS Zone Contributor" \
  --assignee "${IDENTITY_CLIENT_ID}" --scope "${DNS_ID}"
$ az role assignment create --role "Reader" \
  --assignee "${IDENTITY_CLIENT_ID}" --scope "${RESOURCE_GROUP_ID}"
```

#### Create a federated identity credential

A binding between the managed identity and the ExternalDNS service account needs to be setup by creating a federated identity resource:

```bash
OIDC_ISSUER_URL="$(az aks show -n myAKSCluster -g myResourceGroup --query "oidcIssuerProfile.issuerUrl" -otsv)"

az identity federated-credential create --name ${IDENTITY_NAME} --identity-name ${IDENTITY_NAME} --resource-group $AZURE_AKS_RESOURCE_GROUP} --issuer "$OIDC_ISSUER_URL" --subject "system:serviceaccount:default:external-dns"
```

NOTE: make sure federated credential refers to correct namespace and service account (`system:serviceaccount:<NAMESPACE>:<SERVICE_ACCOUNT>`)

#### Helm

When deploying external-dns with Helm you need to create a secret to store the Azure config (see below) and create a workload identity (out of scope here) before you can install the chart.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: external-dns-azure
type: Opaque
data:
  azure.json: |
    {
      "tenantId": "<TENANT_ID>",
      "subscriptionId": "<SUBSCRIPTION_ID>",
      "resourceGroup": "<AZURE_DNS_ZONE_RESOURCE_GROUP>",
      "useWorkloadIdentityExtension": true
    }
```

Once you have created the secret and have a workload identity you can install the chart with the following values.

```yaml
fullnameOverride: external-dns

serviceAccount:
  labels:
    azure.workload.identity/use: "true"
  annotations:
    azure.workload.identity/client-id: <IDENTITY_CLIENT_ID>

podLabels:
  azure.workload.identity/use: "true"

extraVolumes:
  - name: azure-config-file
    secret:
      secretName: external-dns-azure

extraVolumeMounts:
  - name: azure-config-file
    mountPath: /etc/kubernetes
    readOnly: true

provider:
  name: azure
```

NOTE: make sure the pod is restarted whenever you make a configuration change.

#### kubectl (alternative)

##### Create a configuration file for the managed identity

Create the file `azure.json` with the values from previous steps:

```bash
cat <<-EOF > /local/path/to/azure.json
{
  "subscriptionId": "$(az account show --query id -o tsv)",
  "resourceGroup": "$AZURE_DNS_ZONE_RESOURCE_GROUP",
  "useWorkloadIdentityExtension": true
}
EOF
```

NOTE: it's also possible to specify (or override) ClientID specified in the next section through `aadClientId` field in this `azure.json` file.

Use the `azure.json` file to create a Kubernetes secret:

```bash
kubectl create secret generic azure-config-file --namespace "default" --from-file /local/path/to/azure.json
```

##### Update labels and annotations on ExternalDNS service account

To instruct Workload Identity webhook to inject a projected token into the ExternalDNS pod, the pod needs to have a label `azure.workload.identity/use: "true"` (before Workload Identity 1.0.0, this label was supposed to be set on the service account instead).
Also, the service account needs to have an annotation `azure.workload.identity/client-id: <IDENTITY_CLIENT_ID>`:

To patch the existing serviceaccount and deployment, use the following command:

```bash
$ kubectl patch serviceaccount external-dns --namespace "default" --patch \
 "{\"metadata\": {\"annotations\": {\"azure.workload.identity/client-id\": \"${IDENTITY_CLIENT_ID}\"}}}"
$ kubectl patch deployment external-dns --namespace "default" --patch \
 '{"spec": {"template": {"metadata": {"labels": {\"azure.workload.identity/use\": \"true\"}}}}}'
```

NOTE: it's also possible to specify (or override) ClientID through `aadClientId` field in `azure.json`.

NOTE: make sure the pod is restarted whenever you make a configuration change.

## Throttling

When the ExternalDNS managed zones list doesn't change frequently, one can set `--azure-zones-cache-duration` (zones list cache time-to-live). The zones list cache is disabled by default, with a value of 0s.
Also, one can leverage the built-in retry policies of the Azure SDK with a tunable maxRetries value. Environment variable AZURE_SDK_MAX_RETRIES can be specified in the manifest yaml to configure behavior. The defualt value of Azure SDK retry is 3.

## Ingress used with ExternalDNS

This deployment assumes that you will be using nginx-ingress. When using nginx-ingress do not deploy it as a Daemon Set.
This causes nginx-ingress to write the Cluster IP of the backend pods in the ingress status.loadbalancer.ip property which then has external-dns write the Cluster IP(s) in DNS vs. the nginx-ingress service external IP.

Ensure that your nginx-ingress deployment has the following arg: added to it:

```sh
- --publish-service=namespace/nginx-ingress-controller-svcname
```

For more details see here: [nginx-ingress external-dns](https://github.com/kubernetes-sigs/external-dns/blob/HEAD/docs/faq.md#why-is-externaldns-only-adding-a-single-ip-address-in-route-53-on-aws-when-using-the-nginx-ingress-controller-how-do-i-get-it-to-use-the-fqdn-of-the-elb-assigned-to-my-nginx-ingress-controller-service-instead)

## Deploy ExternalDNS

Connect your `kubectl` client to the cluster you want to test ExternalDNS with. Then apply one of the following manifests file to deploy ExternalDNS.

The deployment assumes that ExternalDNS will be installed into the `default` namespace.  If this namespace is different, the `ClusterRoleBinding` will need to be updated to reflect the desired alternative namespace, such as `external-dns`, `kube-addons`, etc.

### Manifest (for clusters without RBAC enabled)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: external-dns
spec:
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: external-dns
  template:
    metadata:
      labels:
        app: external-dns
    spec:
      containers:
      - name: external-dns
        image: registry.k8s.io/external-dns/external-dns:v0.18.0
        args:
        - --source=service
        - --source=ingress
        - --domain-filter=example.com # (optional) limit to only example.com domains; change to match the zone created above.
        - --provider=azure
        - --azure-resource-group=MyDnsResourceGroup # (optional) use the DNS zones from the tutorial's resource group
        - --azure-maxretries-count=1  # (optional) specifies the maxRetires value to be used by the Azure SDK. Default is 3.
        volumeMounts:
        - name: azure-config-file
          mountPath: /etc/kubernetes
          readOnly: true
      volumes:
      - name: azure-config-file
        secret:
          secretName: azure-config-file
```

### Manifest (for clusters with RBAC enabled, cluster access)

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: external-dns
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: external-dns
rules:
  - apiGroups: [""]
    resources: ["services","pods", "nodes"]
    verbs: ["get","watch","list"]
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["get","watch","list"]
  - apiGroups: ["extensions","networking.k8s.io"]
    resources: ["ingresses"]
    verbs: ["get","watch","list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: external-dns-viewer
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: external-dns
subjects:
  - kind: ServiceAccount
    name: external-dns
    namespace: default
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: external-dns
spec:
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: external-dns
  template:
    metadata:
      labels:
        app: external-dns
    spec:
      serviceAccountName: external-dns
      containers:
        - name: external-dns
          image: registry.k8s.io/external-dns/external-dns:v0.18.0
          args:
            - --source=service
            - --source=ingress
            - --domain-filter=example.com # (optional) limit to only example.com domains; change to match the zone created above.
            - --provider=azure
            - --azure-resource-group=MyDnsResourceGroup # (optional) use the DNS zones from the tutorial's resource group
            - --txt-prefix=externaldns-
            - --azure-maxretries-count=1  # (optional) specifies the maxRetires value to be used by the Azure SDK. Default is 3.
          volumeMounts:
            - name: azure-config-file
              mountPath: /etc/kubernetes
              readOnly: true
      volumes:
        - name: azure-config-file
          secret:
            secretName: azure-config-file
```

### Manifest (for clusters with RBAC enabled, namespace access)

This configuration is the same as above, except it only requires privileges for the current namespace, not for the whole cluster.
However, access to [nodes](https://kubernetes.io/docs/concepts/architecture/nodes/) requires cluster access, so when using this manifest,
services with type `NodePort` will be skipped!

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: external-dns
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: external-dns
rules:
  - apiGroups: [""]
    resources: ["services","pods"]
    verbs: ["get","watch","list"]
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["get","watch","list"]
  - apiGroups: ["extensions","networking.k8s.io"]
    resources: ["ingresses"]
    verbs: ["get","watch","list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: external-dns
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: external-dns
subjects:
  - kind: ServiceAccount
    name: external-dns
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: external-dns
spec:
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: external-dns
  template:
    metadata:
      labels:
        app: external-dns
    spec:
      serviceAccountName: external-dns
      containers:
        - name: external-dns
          image: registry.k8s.io/external-dns/external-dns:v0.18.0
          args:
            - --source=service
            - --source=ingress
            - --domain-filter=example.com # (optional) limit to only example.com domains; change to match the zone created above.
            - --provider=azure
            - --azure-resource-group=MyDnsResourceGroup # (optional) use the DNS zones from the tutorial's resource group
            - --azure-maxretries-count=1  # (optional) specifies the maxRetires value to be used by the Azure SDK. Default is 3.
          volumeMounts:
            - name: azure-config-file
              mountPath: /etc/kubernetes
              readOnly: true
      volumes:
        - name: azure-config-file
          secret:
            secretName: azure-config-file
```

Create the deployment for ExternalDNS:

```bash
kubectl create --namespace "default" --filename externaldns.yaml
```

## Ingress Option: Expose an nginx service with an ingress

Create a file called `nginx.yaml` with the following contents:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
spec:
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
        - image: nginx
          name: nginx
          ports:
          - containerPort: 80
---
apiVersion: v1
kind: Service
metadata:
  name: nginx-svc
spec:
  ports:
    - port: 80
      protocol: TCP
      targetPort: 80
  selector:
    app: nginx
  type: ClusterIP

---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: nginx
spec:
  ingressClassName: nginx
  rules:
    - host: server.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: nginx-svc
                port:
                  number: 80
```

When you use ExternalDNS with Ingress resources, it automatically creates DNS records based on the hostnames listed in those Ingress objects.
Those hostnames must match the filters that you defined (if any):

* By default, `--domain-filter` filters Azure DNS zone.
* If you use `--domain-filter` together with `--zone-name-filter`, the behavior changes: `--domain-filter` then filters Ingress domains, not the Azure DNS zone name.

When those hostnames are removed or renamed the corresponding DNS records are also altered.

Create the deployment, service and ingress object:

```bash
kubectl create --namespace "default" --filename nginx.yaml
```

Since your external IP would have already been assigned to the nginx-ingress service, the DNS records pointing to the IP of the nginx-ingress service should be created within a minute.

## Azure Load Balancer option: Expose an nginx service with a load balancer

Create a file called `nginx.yaml` with the following contents:

```yaml
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
spec:
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
        - image: nginx
          name: nginx
          ports:
          - containerPort: 80
---
apiVersion: v1
kind: Service
metadata:
  name: nginx-svc
  annotations:
    external-dns.alpha.kubernetes.io/hostname: server.example.com
spec:
  ports:
    - port: 80
      protocol: TCP
      targetPort: 80
  selector:
    app: nginx
  type: LoadBalancer
```

The annotation `external-dns.alpha.kubernetes.io/hostname` is used to specify the DNS name that should be created for the service. The annotation value is a comma separated list of host names.

## Verifying Azure DNS records

Run the following command to view the A records for your Azure DNS zone:

```bash
az network dns record-set a list --resource-group "MyDnsResourceGroup" --zone-name example.com
```

Substitute the zone for the one created above if a different domain was used.

This should show the external IP address of the service as the A record for your domain ('@' indicates the record is for the zone itself).

## Delete Azure Resource Group

Now that we have verified that ExternalDNS will automatically manage Azure DNS records, we can delete the tutorial's
resource group:

```bash
az group delete --name "MyDnsResourceGroup"
```

## More tutorials

A video explanation is available here: https://www.youtube.com/watch?v=VSn6DPKIhM8&list=PLpbcUe4chE79sB7Jg7B4z3HytqUUEwcNE

![image](https://user-images.githubusercontent.com/6548359/235437721-87611869-75f2-4f32-bb35-9da585e46299.png)
