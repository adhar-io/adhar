#Set personal access token as found in Digital Ocean
variable "do_token" {
  description = "personal access token for digital ocean"
  type        = string
  default     = "REPLACE THIS SENTENCE WITH YOUR DIGITAL OCEAN ACCESS TOKEN"
}

variable "region" {
  description = "region of the digital ocean data center"
  type        = string
}

variable "machine_size" {
  description = "Droplet size of the cluster nodepool"
  type        = string
}

variable "node_count" {
  description = "Cluster nodepool node count"
  type        = number
}

variable "min_nodes" {
  description = "Minimum number of nodes in cluster nodepool"
  type        = number
}

variable "max_nodes" {
  description = "Maximum number of nodes in cluster nodepool"
  type        = number
}
