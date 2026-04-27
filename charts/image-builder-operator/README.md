# image-builder-operator

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.1.0](https://img.shields.io/badge/AppVersion-0.1.0-informational?style=flat-square)

A Helm chart for the Image Builder Operator

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| controllerManager.image.imagePullPolicy | string | `"IfNotPresent"` | Controller manager container image pull policy. |
| controllerManager.image.repository | string | `"ghcr.io/dana-team/image-builder-operator"` | Controller manager container image repository. |
| controllerManager.image.tag | string | `""` | Controller manager container image tag. Defaults to .Chart.AppVersion if empty. |
| controllerManager.replicas | int | `1` | Number of replicas for the controller manager Deployment. |
| controllerManager.resources | object | `{"limits":{"cpu":"500m","memory":"128Mi"},"requests":{"cpu":"10m","memory":"64Mi"}}` | Resource requests/limits for the controller manager container. |
| imageBuildPolicy | object | `{"clusterBuildStrategy":{"buildFile":{"absent":"buildpacks-v3","present":"buildah-strategy-managed-push"}},"enabled":true,"retention":{"failedLimit":5,"succeededLimit":10}}` | Configuration for ImageBuildPolicy CRD |
| imageBuildPolicy.clusterBuildStrategy.buildFile.absent | string | `"buildpacks-v3"` | Strategy name when the source does not indicate a file-based build. |
| imageBuildPolicy.clusterBuildStrategy.buildFile.present | string | `"buildah-strategy-managed-push"` | Strategy name when the source indicates a file-based build. |
| imageBuildPolicy.enabled | bool | `true` | Enable or disable creation of the ImageBuildPolicy resource by Helm. |
| imageBuildPolicy.retention | object | `{"failedLimit":5,"succeededLimit":10}` | Default completed-build retention on the ImageBuildPolicy (spec.retention). Set to null to omit. |
| onCommit.enabled | bool | `false` | Enable OnCommit webhook triggers and associated service. |

