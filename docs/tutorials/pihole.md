# Pi-hole

This tutorial describes how to setup ExternalDNS to sync records with Pi-hole's Custom DNS.
Pi-hole has an internal list it checks last when resolving requests. This list can contain any number of arbitrary A, AAAA or CNAME records.
There is a pseudo-API exposed that ExternalDNS is able to use to manage these records.

__NOTE:__ Your Pi-hole must be running [version 5.9 or newer](https://pi-hole.net/blog/2022/02/12/pi-hole-ftl-v5-14-web-v5-11-and-core-v5-9-released).

__NOTE:__ Provider for Pi-hole version prior to 6.0 is now deprecated and will be removed in future release.

__NOTE:__ Since Pi-hole version 6, you should use the flag *--pihole-api-version=6*

## Deploy ExternalDNS

You can skip to the [manifest](#externaldns-manifest) if authentication is disabled on your Pi-hole instance or you don't want to use secrets.

If your Pi-hole server's admin dashboard is protected by a password, you'll likely want to create a secret first containing its value.
This is optional since you *do* retain the option to pass it as a flag with `--pihole-password`.

You can create the secret with:

```bash
kubectl create secret generic pihole-password \
    --from-literal EXTERNAL_DNS_PIHOLE_PASSWORD=supersecret
```

Replacing __"supersecret"__ with the actual password to your Pi-hole server.

### ExternalDNS Manifest

Apply the following manifest to deploy ExternalDNS, editing values for your environment accordingly.
Be sure to change the namespace in the `ClusterRoleBinding` if you are using a namespace other than __default__.

```yaml
---
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
  resources: ["services","pods"]
  verbs: ["get","watch","list"]
- apiGroups: ["discovery.k8s.io"]
  resources: ["endpointslices"]
  verbs: ["get","watch","list"]
- apiGroups: ["extensions","networking.k8s.io"]
  resources: ["ingresses"]
  verbs: ["get","watch","list"]
- apiGroups: [""]
  resources: ["nodes"]
  verbs: ["list","watch"]
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
        # If authentication is disabled and/or you didn't create
        # a secret, you can remove this block.
        envFrom:
        - secretRef:
            # Change this if you gave the secret a different name
            name: pihole-password
        args:
        - --source=service
        - --source=ingress
        # Pihole only supports A/AAAA/CNAME records so there is no mechanism to track ownership.
        # You don't need to set this flag, but if you leave it unset, you will receive warning
        # logs when ExternalDNS attempts to create TXT records.
        - --registry=noop
        # IMPORTANT: If you have records that you manage manually in Pi-hole, set
        # the policy to upsert-only so they do not get deleted.
        - --policy=upsert-only
        - --provider=pihole
        # Switch to pihole V6 API
        - --pihole-api-version=6
        # Change this to the actual address of your Pi-hole web server
        - --pihole-server=http://pihole-web.pihole.svc.cluster.local
      securityContext:
        fsGroup: 65534 # For ExternalDNS to be able to read Kubernetes token files
```

### Arguments

- `--pihole-server (env: EXTERNAL_DNS_PIHOLE_SERVER)` - The address of the Pi-hole web server
- `--pihole-password (env: EXTERNAL_DNS_PIHOLE_PASSWORD)` - The password to the Pi-hole web server (if enabled)
- `--pihole-tls-skip-verify (env: EXTERNAL_DNS_PIHOLE_TLS_SKIP_VERIFY)` - Skip verification of any TLS certificates served by the Pi-hole web server.
- `--pihole-api-version (env: EXTERNAL_DNS_PIHOLE_API_VERSION)` - Specify the pihole API version (default is 5. Eligible values are 5 or 6).

## Verify ExternalDNS Works

### Ingress Example

Create an Ingress resource. ExternalDNS will use the hostname specified in the Ingress object.

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: foo
spec:
  ingressClassName: nginx
  rules:
  - host: foo.bar.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: foo
            port:
              number: 80
```

### Service Example

The below sample application can be used to verify Services work.
For services ExternalDNS will look for the annotation `external-dns.alpha.kubernetes.io/hostname` on the service and use the corresponding value.

```yaml
---
apiVersion: v1
kind: Service
metadata:
  name: nginx
  annotations:
    external-dns.alpha.kubernetes.io/hostname: nginx.external-dns-test.homelab.com
spec:
  type: LoadBalancer
  ports:
  - port: 80
    name: http
    targetPort: 80
  selector:
    app: nginx
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
          name: http
```

You can then query your Pi-hole to see if the record was created.

Change *@192.168.100.2* to the actual address of your DNS server

```bash
$ dig +short @192.168.100.2  nginx.external-dns-test.homelab.com

192.168.100.129
```
