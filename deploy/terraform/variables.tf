variable "cluster_name" {
  description = "Kubernetes cluster name"
  type        = string
  default     = "fusion-gateway"
}

variable "namespace" {
  description = "Kubernetes namespace for fusion-gateway"
  type        = string
  default     = "fusion-gateway"
}

variable "region" {
  description = "Cloud provider region"
  type        = string
  default     = "us-west-2"
}

variable "image_repository" {
  description = "Container image repository"
  type        = string
  default     = "fusion-gateway"
}

variable "image_tag" {
  description = "Container image tag"
  type        = string
  default     = "latest"
}

variable "replica_count" {
  description = "Number of gateway replicas"
  type        = number
  default     = 2
}

variable "master_key" {
  description = "Fusion-Gateway master API key"
  type        = string
  sensitive   = true
  default     = "CHANGE_ME"
}

variable "enable_hpa" {
  description = "Enable HorizontalPodAutoscaler"
  type        = bool
  default     = true
}

variable "enable_ingress" {
  description = "Enable Ingress resource"
  type        = bool
  default     = true
}

variable "ingress_host" {
  description = "Ingress hostname"
  type        = string
  default     = "fusion-gateway.example.com"
}

variable "enable_pdb" {
  description = "Enable PodDisruptionBudget"
  type        = bool
  default     = true
}
