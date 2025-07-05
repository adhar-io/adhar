import ../common

# Gitea KCL configuration template
schema GiteaConfig:
    """Configuration for Gitea Git server"""
    enableHAMode: bool = True
    namespace: str = "adhar-system"
    serviceType: str = "ClusterIP"

# Generate local overlay configuration for Gitea
giteaLocalOverlay = {
    replicas = 1
    resources = common.localResourceConstraints
    postgresql = {
        replicas = 1
        resources = common.localResourceConstraints
    }
    redis = {
        replicas = 1 
        resources = common.localResourceConstraints
    }
}

# Generate production overlay configuration for Gitea
giteaProductionOverlay = {
    replicas = 2
    resources = common.productionResourceConstraints
    postgresql = {
        replicas = 2
        resources = common.productionResourceConstraints
    }
    redis = {
        replicas = 3
        resources = common.productionResourceConstraints
    }
}

# Get overlay configuration based on HA mode
getGiteaOverlay = lambda enableHAMode: bool -> any {
    if enableHAMode:
        giteaProductionOverlay
    else:
        giteaLocalOverlay
}
