variable "namespace" {
  description = "Kubernetes namespace"
  type        = string
  default     = "fusion-gateway"
}

variable "chart_path" {
  description = "Local path to Helm chart"
  type        = string
}

variable "replica_count" {
  description = "Number of replicas"
  type        = number
  default     = 2
}

variable "image_repository" {
  description = "Image repository"
  type        = string
  default     = "fusion-gateway"
}

variable "image_tag" {
  description = "Image tag"
  type        = string
  default     = "latest"
}

variable "master_key" {
  description = "Master API key"
  type        = string
  sensitive   = true
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

variable "ingress_host" {
  description = "Ingress hostname"
  type        = string
  default     = "fusion-gateway.example.com"
}
