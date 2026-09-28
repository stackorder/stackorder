mock_resource "random_password" {
  defaults = {
    result = "mockdatabasepassword"
  }
}

mock_resource "random_bytes" {
  defaults = {
    hex = "0000000000000000000000000000000000000000000000000000000000000000"
  }
}
