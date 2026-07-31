provider "google" {
  project = var.project_id
  region  = var.region
}

data "google_client_config" "default" {}

module "gke" {
  source  = "terraform-google-modules/kubernetes-engine/google//modules/autopilot"
  version = "~> 29.0"

  project_id  = var.project_id
  name        = var.cluster_name
  region      = var.region
  network     = "${var.cluster_name}-vpc"
  subnetwork  = "${var.cluster_name}-subnet"

  ip_range_pods          = "pods"
  ip_range_services      = "services"
  enable_private_nodes   = true
  enable_private_endpoint = false

  release_channel = "REGULAR"
}

provider "kubernetes" {
  host                   = "https://${module.gke.endpoint}"
  token                  = data.google_client_config.default.access_token
  cluster_ca_certificate = base64decode(module.gke.ca_certificate)
}

provider "helm" {
  kubernetes {
    host                   = "https://${module.gke.endpoint}"
    token                  = data.google_client_config.default.access_token
    cluster_ca_certificate = base64decode(module.gke.ca_certificate)
  }
}
