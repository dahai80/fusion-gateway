# Plan: K8s Infrastructure Gap Fill (HPA / PDB / Ingress / Terraform)

## Gap Analysis

| Capability | Current | Target |
|-----------|---------|--------|
| HPA | ❌ None | ✅ CPU/Memory + custom metrics |
| PDB | ❌ None | ✅ MinAvailable 1 (or percentage) |
| Ingress | ❌ None | ✅ Nginx Ingress with TLS |
| Secret | ❌ master_key in ConfigMap | ✅ K8s Secret for sensitive config |
| ServiceAccount | ❌ None | ✅ Dedicated SA with minimal RBAC |
| Terraform | ❌ None | ✅ AWS (EKS) + GCP (GKE) modules |
| NetworkPolicy | ❌ None | ✅ Deny-all + allow intra-namespace |

## File Plan

### 1. K8s Raw Manifests (`deploy/kubernetes/`)

| File | Content |
|------|---------|
| `namespace.yaml` | Namespace `fusion-gateway` |
| `serviceaccount.yaml` | SA + RBAC (get/list/watch ConfigMaps for hot reload) |
| `secret.yaml` | Sensitive config keys (master_key, api_keys) |
| `hpa.yaml` | HPA: minReplicas=2, maxReplicas=10, CPU 70% / Memory 80% targets |
| `pdb.yaml` | PDB: minAvailable 1 (or 50%) |
| `ingress.yaml` | Nginx Ingress with TLS, path `/` → Service :8100 |
| `networkpolicy.yaml` | Deny all ingress except within namespace + from ingress controller |
| Update `deployment.yaml` | Add SA, secretRef, topologySpreadConstraints |
| Update `configmap.yaml` | Remove sensitive keys (moved to Secret) |

### 2. Helm Chart (`deploy/helm/fusion-gateway/`)

| File | Content |
|------|---------|
| Update `values.yaml` | Add HPA, PDB, Ingress, Secret, ServiceAccount, NetworkPolicy blocks |
| `templates/hpa.yaml` | Conditionally rendered HPA |
| `templates/pdb.yaml` | Conditionally rendered PDB |
| `templates/ingress.yaml` | Conditionally rendered Ingress |
| `templates/secret.yaml` | Conditionally rendered Secret |
| `templates/serviceaccount.yaml` | SA + RBAC |
| `templates/networkpolicy.yaml` | Conditionally rendered NetworkPolicy |
| Update `templates/deployment.yaml` | SA, secretRef, topologySpread, annotations |
| Update `Chart.yaml` | Bump version to 0.6.0 |
| Update `_helpers.tpl` | Add common labels, selector labels |

### 3. Terraform (`deploy/terraform/`)

| File | Content |
|------|---------|
| `versions.tf` | Terraform >= 1.5, providers (aws, google, kubernetes, helm) |
| `variables.tf` | Common variables (cluster_name, region, namespace, image, replicas) |
| `outputs.tf` | Endpoint URLs, Helm release status |
| `aws/main.tf` | EKS cluster (existing or create), VPC, IAM, NodeGroup |
| `aws/eks.tf` | EKS module + managed node group (m-series for Apple Silicon inference) |
| `aws/irsa.tf` | IAM Role for ServiceAccount (IRSA) |
| `gcp/main.tf` | GKE Autopilot cluster |
| `gcp/gke.tf` | GKE module + node pool |
| `modules/helm-release/main.tf` | Helm release using fusion-gateway chart |
| `modules/helm-release/variables.tf` | Chart values as variables |

### 4. README Update

Add K8s Infrastructure section documenting HPA, PDB, Ingress, Terraform usage.

## Key Design Decisions

1. **HPA custom metrics**: Use standard CPU/Memory as baseline. Custom metrics (request latency, QPS) require Prometheus Adapter — documented as optional.
2. **PDB strategy**: `minAvailable: 1` for small clusters, percentage for large. Configurable via Helm values.
3. **Ingress**: Nginx Ingress as default (most common). TLS via cert-manager annotation as option.
4. **Secret vs ConfigMap**: Only `master_key` and `api_keys` go to Secret. Rest stays ConfigMap (hot reload friendly).
5. **Terraform**: Focus on EKS (AWS) and GKE (GCP) as the two major cloud K8s providers. No Azure for now. Both reuse the Helm chart via `helm_release`.
6. **No DB Migration**: Fusion-Gateway is stateless (no DB), so no migration Job needed. Read Replica N/A.
7. **Topology spread**: Add `topologySpreadConstraints` to Deployment for even pod distribution across zones.

## Execution Order

1. K8s raw manifests (HPA + PDB + Ingress + Secret + SA + NetworkPolicy)
2. Helm chart updates (all templates + values.yaml + Chart.yaml bump)
3. Terraform modules (AWS + GCP + Helm release)
4. README update
5. Verify: `helm lint` + `helm template` rendering
