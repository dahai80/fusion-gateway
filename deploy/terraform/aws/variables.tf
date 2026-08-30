variable "region" {
  description = "AWS region"
  type        = string
  default     = "us-west-2"
}

variable "cluster_name" {
  description = "EKS cluster name"
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
  description = "Fusion-Gateway master key (>=32 chars). Empty = ship without a master_key. The gateway rejects known placeholders at startup."
  type        = string
  sensitive   = true
  default     = ""
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
