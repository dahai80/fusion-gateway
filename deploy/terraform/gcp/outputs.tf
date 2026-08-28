output "cluster_name" {
  value = module.gke.name
}

output "cluster_endpoint" {
  value = module.gke.endpoint
}

output "gateway_endpoint" {
  value = var.enable_ingress ? "https://${var.ingress_host}" : "http://<cluster-ip>:11432"
}
