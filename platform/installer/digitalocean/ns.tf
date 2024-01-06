# Create a namespace for the adhar system
resource "kubernetes_namespace" "adhar_system" {
  depends_on = [ digitalocean_kubernetes_cluster.adhar_mgmt_k8s_cluster ]

  metadata {
    name = "adhar-system"
  }
}