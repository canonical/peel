{
  "image": "{{ config_get("user.oci.image", "")|escapejs }}",
  "platform": "{{ config_get("user.oci.platform", "")|escapejs }}",
  "insecure": "{{ config_get("user.oci.insecure", "false")|escapejs }}",
  "username": "{{ config_get("user.oci.username", "")|escapejs }}",
  "password": "{{ config_get("user.oci.password", "")|escapejs }}",
  "auth": "{{ config_get("user.oci.auth", "")|escapejs }}",
  "identity_token": "{{ config_get("user.oci.identity_token", "")|escapejs }}",
  "registry_token": "{{ config_get("user.oci.registry_token", "")|escapejs }}",
  "working_dir": "{{ config_get("user.oci.working_dir", "")|escapejs }}",
  "user": "{{ config_get("user.oci.user", "")|escapejs }}",
  "entrypoint": {{ config_get("user.oci.entrypoint", "[]")|safe }},
  "cmd": {{ config_get("user.oci.cmd", "[]")|safe }},
  "env": {{ config_get("user.oci.env", "[]")|safe }}
}
