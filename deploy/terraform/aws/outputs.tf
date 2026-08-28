output "cluster_endpoint" {
  value = module.eks.cluster_endpoint
}

output "cluster_name" {
  value = module.eks.cluster_name
}

output "gateway_endpoint" {
  value = var.enable_ingress ? "https://${var.ingress_host}" : "http://<cluster-ip>:11432"
}
