data "digitalocean_kubernetes_cluster" "cluster_info" {
  name = "adhar-mgmt-k8s-cluster"
  depends_on = [
    kubernetes_namespace.adhar_system
  ]
}

# Installs Adhar chart on cluster
# Note: This resource waits until all the jobs have finished installing.
# Estimated time to finish for vanilla Adhar: 15 ~ 20min 
# If it takes longer than 20 minutes you might want to check the kubernetes dashboard for status 
resource "helm_release" "adhar" {
  name = "adhar"

  repository = "https://chart.adhar.io"
  chart      = "adhar"
  namespace  = "adhar-system"

  values        = [file("adhar-values.yaml")]
  timeout       = 1800
  wait_for_jobs = true
}

resource "null_resource" "print_adhar_url" {
  depends_on = [
    helm_release.adhar
  ]
  provisioner "local-exec" {
    command = "kubectl logs jobs/adhar -n adhar-system -f"
  }
}