resource "kubernetes_namespace" "gateway" {
  metadata {
    name = var.namespace
    labels = {
      app.kubernetes.io/name      = "fusion-gateway"
      app.kubernetes.io/part-of   = "fusion-gateway"
    }
  }
}

resource "helm_release" "gateway" {
  name       = "fusion-gateway"
  repository = ""
  chart      = var.chart_path
  namespace  = kubernetes_namespace.gateway.metadata[0].name

  values = [
    yamlencode({
      replicaCount = var.replica_count
      image = {
        repository = var.image_repository
        tag        = var.image_tag
      }
      secrets = {
        master_key = var.master_key
      }
      ingress = {
        enabled = var.enable_ingress
        className = "nginx"
        hosts = [{
          host  = var.ingress_host
          paths = [{ path = "/", pathType = "Prefix" }]
        }]
        tls = var.enable_ingress ? [{
          secretName = "fusion-gateway-tls"
          hosts      = [var.ingress_host]
        }] : []
      }
      hpa = {
        enabled                              = var.enable_hpa
        minReplicas                          = 2
        maxReplicas                          = 10
        targetCPUUtilizationPercentage       = 70
        targetMemoryUtilizationPercentage    = 80
      }
      pdb = {
        enabled      = var.enable_pdb
        minAvailable = 1
      }
    })
  ]
}
