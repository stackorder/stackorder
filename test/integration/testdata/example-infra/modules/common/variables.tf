variable "name" {
  description = "Name of the component being tagged."
  type        = string
}

variable "environment" {
  description = "Environment the component belongs to."
  type        = string
  default     = "example"
}

variable "extra_tags" {
  description = "Tags merged over the defaults."
  type        = map(string)
  default     = {}
}
