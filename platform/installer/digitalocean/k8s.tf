# Get latest minor version slug from DO 
data "digitalocean_kubernetes_versions" "k8s_version_slug" {
  version_prefix = "1.27."
}

# Provision k8s cluster
# Note: Also sets the created cluster as default in your local kube config 
# Note: For reason stated above you should run terraform as root with sudo -s
#       Or comment out the local-exec block if you don't want this cluster as your default cluster
resource "digitalocean_kubernetes_cluster" "adhar_mgmt_k8s_cluster" {
  name    = "adhar-mgmt-k8s-cluster"
  region  = var.region
  version = data.digitalocean_kubernetes_versions.k8s_version_slug.latest_version

  node_pool {
    name       = "adhar-node-pool"
    size       = var.machine_size
    node_count = var.node_count
    auto_scale = true
    min_nodes  = var.min_nodes
    max_nodes  = var.max_nodes
  }

  provisioner "local-exec" {
    command = "doctl kubernetes cluster kubeconfig save ${digitalocean_kubernetes_cluster.adhar_mgmt_k8s_cluster.id}"
  }
}