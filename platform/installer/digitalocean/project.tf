# Assigns cluster to your project
# Note: Expects that your project already exists.
#       If you want your project to be managed (and also be destroyed!) by terraform 
#       You can comment out the data and resource block and uncomment the playground block
resource "digitalocean_project" "adhar_project_folder" {
  name        = "adhar"
  description = "Adhar - Open Platform for Modern Businesses"
  purpose     = "Web Application"
  environment = "Development"
}

resource "digitalocean_project_resources" "adhar_project" {
  project   = digitalocean_project.adhar_project_folder.id
  resources = [digitalocean_kubernetes_cluster.adhar_mgmt_k8s_cluster.urn]
  depends_on = [
    digitalocean_project.adhar_project_folder
  ]
}