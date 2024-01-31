data "digitalocean_kubernetes_cluster" "cluster_info" {
  name = "adhar-mgmt-k8s-cluster"

  depends_on = [
    digitalocean_kubernetes_cluster.adhar_mgmt_k8s_cluster
  ]
}

# Installs Adhar chart on cluster
# Note: This resource waits until all the jobs have finished installing.
# Estimated time to finish for vanilla Adhar: 30 ~ 40min 
# If it takes longer than 40 minutes you might want to check the kubernetes dashboard for status 
resource "helm_release" "adhar" {
  name = "adhar"

  repository = "https://chart.adhar.io"
  chart      = "adhar"

  values        = [file("values.yaml")]
  timeout       = 3600 # 1 hour
  wait_for_jobs = true
}

resource "null_resource" "print_adhar_url" {
  depends_on = [
    helm_release.adhar
  ]
  provisioner "local-exec" {
    command = "kubectl logs jobs/adhar -n default -f"
  }
}