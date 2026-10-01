variable "project_id" { type = string }
variable "region" {
  type    = string
  default = "asia-south1"
}
variable "environment" {
  type    = string
  default = "prod"
}
variable "public_api_endpoint" {
  type    = bool
  default = false
}
variable "machine_type" {
  type    = string
  default = "n2-standard-8"
}
variable "node_count" {
  type    = number
  default = 2
}
variable "node_max" {
  type    = number
  default = 10
}
variable "db_tier" {
  type    = string
  default = "db-custom-8-32768"
}
variable "redis_gb" {
  type    = number
  default = 16
}
