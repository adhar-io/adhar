data "civo_kubernetes_cluster" "cluster_info" {
  name = "adhar-cluster-info"
  depends_on = [
    civo_kubernetes_cluster.adhar_mgmt_k8s_cluster
  ]
}

# Installs Otomi chart on cluster
# Note: This resource waits until all the jobs have finished installing.
# Estimated time to finish for vanilla Otomi: 15 ~ 20min 
# If it takes longer than 20 minutes you might want to check the kubernetes dashboard for status 
resource "helm_release" "otomi" {
  name = "otomi"

  repository = "https://otomi.io/otomi-core"
  chart      = "otomi"

  values        = [file("adhar-values.yaml")]
  timeout       = 1800
  wait_for_jobs = true
}

resource "null_resource" "print_otomi_url" {
  depends_on = [
    helm_release.otomi
  ]
  provisioner "local-exec" {
    command = "kubectl logs jobs/otomi -n default -f"
  }
}