terraform {
  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.0"
    }
  }
}

# Configure the DigitalOcean Provider
provider "digitalocean" {
  token = var.do_token
}

provider "kubernetes" {
  config_path = "~/.kube/config"
}

provider "helm" {
  kubernetes {
    host                   = digitalocean_kubernetes_cluster.adhar_mgmt_k8s_cluster.endpoint
    token                  = digitalocean_kubernetes_cluster.adhar_mgmt_k8s_cluster.kube_config.0.token
    client_certificate     = base64decode(digitalocean_kubernetes_cluster.adhar_mgmt_k8s_cluster.kube_config.0.client_certificate)
    client_key             = base64decode(digitalocean_kubernetes_cluster.adhar_mgmt_k8s_cluster.kube_config.0.client_key)
    cluster_ca_certificate = base64decode(digitalocean_kubernetes_cluster.adhar_mgmt_k8s_cluster.kube_config.0.cluster_ca_certificate)
  }
}