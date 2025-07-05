# Adhar Platform Apps Configuration
# This file defines configuration for all platform applications

# Global settings
global_config = {
    namespace = "adhar-system"
    cluster_domain = "cluster.local"
}

# Resource configurations for different modes
local_resources = {
    cpu_request = "100m"
    memory_request = "256Mi"
    cpu_limit = "500m"
    memory_limit = "512Mi"
}

production_resources = {
    cpu_request = "500m"
    memory_request = "1Gi"
    cpu_limit = "2"
    memory_limit = "4Gi"
}

# Gitea configuration
gitea_config = {
    local = {
        replicas = 1
        resources = local_resources
        postgresql_replicas = 1
        redis_replicas = 1
    }
    production = {
        replicas = 2
        resources = production_resources
        postgresql_replicas = 2
        redis_replicas = 3
    }
}

# ArgoCD configuration
argocd_config = {
    local = {
        server_replicas = 1
        controller_replicas = 1
        resources = local_resources
    }
    production = {
        server_replicas = 2
        controller_replicas = 2
        resources = production_resources
    }
}

# Nginx Ingress configuration
nginx_config = {
    local = {
        replicas = 1
        resources = local_resources
    }
    production = {
        replicas = 2
        resources = production_resources
    }
}

# Cilium configuration
cilium_config = {
    local = {
        operator_replicas = 1
        hubble_relay_replicas = 1
        hubble_ui_replicas = 1
        resources = local_resources
        enable_encryption = False
        enable_l7_proxy = False
    }
    production = {
        operator_replicas = 2
        hubble_relay_replicas = 2
        hubble_ui_replicas = 2
        resources = production_resources
        enable_encryption = True
        enable_l7_proxy = True
    }
}

# Crossplane configuration
crossplane_config = {
    local = {
        replicas = 1
        resources = local_resources
    }
    production = {
        replicas = 2
        resources = production_resources
    }
}
