terraform {
  required_providers {
    civo = {
      source = "civo/civo"
    }
  }
}

# Configure the Civo Provider
provider "civo" {
  token = var.civo_token
  region = var.region
}

provider "helm" {
  kubernetes {
    host                   = civo_kubernetes_cluster.adhar_mgmt_k8s_cluster.endpoint
    token                  = civo_kubernetes_cluster.adhar_mgmt_k8s_cluster.kube_config.0.token
    client_certificate     = base64decode(civo_kubernetes_cluster.adhar_mgmt_k8s_cluster.kube_config.0.client_certificate)
    client_key             = base64decode(civo_kubernetes_cluster.adhar_mgmt_k8s_cluster.kube_config.0.client_key)
    cluster_ca_certificate = base64decode(civo_kubernetes_cluster.adhar_mgmt_k8s_cluster.kube_config.0.cluster_ca_certificate)
  }
}