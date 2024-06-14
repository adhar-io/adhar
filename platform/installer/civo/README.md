# adhar-civo-terraform-installer

Creates the following:

- A digital ocean project called 'Adhar'
- Kubernetes cluster with 2 nodes each having 8vcpu and 16gb ram
- Otomi helm release that installs Otomi core on the cluster

Make sure to have [terraform](https://developer.hashicorp.com/terraform/tutorials/aws-get-started/install-cli), [civo cli](https://www.civo.com/docs/overview/civo-cli/) and [kubectl](https://kubernetes.io/docs/tasks/tools/) installed on your local machine.

Don't forget to fill in your digital ocean access key in `terraform.tfvars`

Also make sure to run the project as a superuser with `sudo -s`, otherwise the terraform script is not allowed to execute the `civo` command.
