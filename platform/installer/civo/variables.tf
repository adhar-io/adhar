#Set personal access token as found in Digital Ocean
variable "civo_token" {
  description = "personal access token for civo"
  type        = string
  default     = "REPLACE THIS SENTENCE WITH YOUR DIGITAL OCEAN ACCESS TOKEN"
}

variable "region" {
  description = "region of the civo data center"
  type        = string
}

variable "machine_size" {
  description = "Machine size of the cluster nodepool"
  type        = string
}
