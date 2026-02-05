# Git webhook user guide (OnCommit)

Use this guide to configure GitHub or GitLab webhooks to trigger `ImageBuild`
rebuilds on push events.

## Prerequisites

- Operator deployed with `onCommit.enabled=true`.
- Webhook endpoint reachable over HTTPS.
- A Git repository you control (GitHub or GitLab).

## Webhook URL

The operator exposes `/webhooks/git` via the git webhook service.

Use the public HTTPS URL for that service when configuring your Git provider.

## Configure GitHub / GitLab webhook

- **Payload URL**: the externally reachable HTTPS route for the webhook
- **Content type**: `application/json`
- **Secret**: same value as `webhookSecretRef`
- **Events**: push events only
- **SSL/TLS**: use a valid certificate in production

## Create the webhook secret

Create a Kubernetes Secret that stores the webhook token from your Git provider.
This Secret must be referenced by your `ImageBuild` via
`spec.onCommit.webhookSecretRef`.

```bash
kubectl create secret generic git-webhook-secret \
  --from-literal=token=<your-webhook-secret> \
  -n <imagebuild-namespace>
```

Reference the Secret from your `ImageBuild`:

```yaml
apiVersion: build.dana.io/v1alpha1
kind: ImageBuild
metadata:
  name: my-app-build
  namespace: <imagebuild-namespace>
spec:
  rebuild:
    mode: OnCommit
  onCommit:
    webhookSecretRef:
      name: git-webhook-secret
      key: token
  # ... other fields
```

## Expected outcome

On each push to the configured repository and branch:

- The webhook is delivered to the operator.
- The matching `ImageBuild` triggers a rebuild.

