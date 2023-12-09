# Get latest minor version slug from Civo 
data "civo_kubernetes_version" "k8s_version_slug" {
  filter {
    key    = "version"
    values = ["1.27"]
  }
}

# Provision k8s cluster with k3s
# Note: Also sets the created cluster as default in your local kube config 
# Note: For reason stated above you should run terraform as root with sudo -s
#       Or comment out the local-exec block if you don't want this cluster as your default cluster
resource "civo_kubernetes_cluster" "adhar_mgmt_k8s_cluster" {
    name = "adhar_mgmt_k8s_cluster"
    firewall_id = civo_firewall.adhar-firewall.id
    cluster_type = "k3s"
    kubernetes_version = data.civo_kubernetes_version.k8s_version_slug.versions

    pools {
        label = "adhar-node-pool"
        size = var.machine_size
        node_count = 3
    }

    provisioner "local-exec" {
      command = "civo kubernetes cluster kubeconfig save ${civo_kubernetes_cluster.adhar_mgmt_k8s_cluster.id}"
    }
}