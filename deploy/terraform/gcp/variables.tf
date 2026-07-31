variable "project_id" {
  description = "GCP project ID"
  type        = string
}

variable "region" {
  description = "GCP region"
  type        = string
  default     = "us-west1"
}

variable "cluster_name" {
  description = "GKE cluster name"
  type        = string
  default     = "fusion-gateway"
}

variable "namespace" {
  description = "Kubernetes namespace"
  type        = string
  default     = "fusion-gateway"
}

variable "replica_count" {
  description = "Number of gateway replicas"
  type        = number
  default     = 2
}

variable "master_key" {
  description = "Fusion-Gateway master key"
  type        = string
  sensitive   = true
  default     = "CHANGE_ME"
}

variable "ingress_host" {
  description = "Ingress hostname"
  type        = string
  default     = "fusion-gateway.example.com"
}

variable "enable_hpa" {
  type    = bool
  default = true
}

variable "enable_ingress" {
  type    = bool
  default = true
}

variable "enable_pdb" {
  type    = bool
  default = true
}
