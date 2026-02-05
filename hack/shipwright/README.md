## Shipwright (vendored manifests)

Used by `make install-shipwright`.

- `certs.yaml`: cert-manager webhook certs for Shipwright Build.
- `strategies.yaml`:
  - `buildah-strategy-managed-push` → for `ImageBuildPolicy.spec.clusterBuildStrategy.buildFile.present`
  - `buildpacks-v3` → for `ImageBuildPolicy.spec.clusterBuildStrategy.buildFile.absent`
