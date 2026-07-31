output "helm_release_status" {
  description = "Helm release status"
  value       = helm_release.gateway.status
}

output "namespace" {
  description = "Deployed namespace"
  value       = kubernetes_namespace.gateway.metadata[0].name
}
