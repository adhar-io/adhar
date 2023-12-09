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
