output "namespace" {
  description = "Kubernetes namespace"
  value       = var.namespace
}

output "helm_release_status" {
  description = "Helm release status"
  value       = module.helm_release.helm_release_status
}

output "gateway_endpoint" {
  description = "Gateway endpoint URL"
  value       = var.enable_ingress ? "https://${var.ingress_host}" : "http://<cluster-ip>:8100"
}
